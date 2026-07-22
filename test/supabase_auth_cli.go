package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

type session struct {
	AccessToken  string
	RefreshToken string
}

func main() {
	baseURL := firstNonEmpty(os.Getenv("MAIDPIC_SUPABASE_URL"), os.Getenv("SUPABASE_URL"))
	apiKey := firstNonEmpty(os.Getenv("MAIDPIC_SUPABASE_API_KEY"), os.Getenv("SUPABASE_API_KEY"))
	if baseURL == "" || apiKey == "" {
		fmt.Fprintln(os.Stderr, "缺少 Supabase 配置：请设置 MAIDPIC_SUPABASE_URL 和 MAIDPIC_SUPABASE_API_KEY。")
		os.Exit(1)
	}

	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "打开终端失败：%v\n", err)
		os.Exit(1)
	}
	defer tty.Close()
	reader := bufio.NewReader(tty)

	fmt.Fprint(tty, "Supabase 用户名（通常为邮箱）: ")
	email, err := reader.ReadString(10)
	if err != nil {
		fmt.Fprintf(os.Stderr, "读取用户名失败：%v\n", err)
		os.Exit(1)
	}
	fmt.Fprint(tty, "Supabase 密码: ")
	password, err := readPassword(tty, reader)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n读取密码失败：%v\n", err)
		os.Exit(1)
	}
	fmt.Fprintln(tty)

	current, err := requestSession(baseURL, apiKey, "token?grant_type=password", map[string]string{"email": strings.TrimSpace(email), "password": password})
	if err != nil {
		fmt.Fprintf(os.Stderr, "登录失败：%v\n", err)
		os.Exit(1)
	}
	fmt.Fprintln(tty, "登录成功。")
	printSession(tty, current)
	fmt.Fprintln(tty, "\n按 / 校验 token；按 . 刷新 token；按 q 或 Ctrl-C 退出。")

	restore, err := makeRaw(tty)
	if err != nil {
		fmt.Fprintf(os.Stderr, "进入按键模式失败：%v\n", err)
		os.Exit(1)
	}
	defer restore()
	for {
		key, err := reader.ReadByte()
		if err != nil {
			fmt.Fprintf(os.Stderr, "\n读取按键失败：%v\n", err)
			return
		}
		switch key {
		case "/"[0]:
			fmt.Fprintln(tty)
			valid, err := validateAccessToken(baseURL, apiKey, current.AccessToken)
			if err != nil {
				fmt.Fprintf(tty, "Token 有效性检测失败：%v\n", err)
			} else if valid {
				fmt.Fprintln(tty, "Token 有效性检测结果：有效")
			} else {
				fmt.Fprintln(tty, "Token 有效性检测结果：无效")
			}
			printSession(tty, current)
			fmt.Fprint(tty, "\n按 / 校验 token；按 . 刷新 token；按 q 退出。")
		case "."[0]:
			fmt.Fprintln(tty)
			refreshed, err := requestSession(baseURL, apiKey, "token?grant_type=refresh_token", map[string]string{"refresh_token": current.RefreshToken})
			if err != nil {
				fmt.Fprintf(tty, "Refresh token 获取新 token 失败：%v\n", err)
			} else {
				current = refreshed
				fmt.Fprintln(tty, "Refresh token 获取新 token 成功。")
			}
			printSession(tty, current)
			fmt.Fprint(tty, "\n按 / 校验 token；按 . 刷新 token；按 q 退出。")
		case "q"[0], "Q"[0], 3:
			fmt.Fprintln(tty, "\n已退出。")
			return
		}
	}
}

func requestSession(baseURL, apiKey, endpoint string, payload map[string]string) (session, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return session{}, err
	}
	request, err := http.NewRequest(http.MethodPost, authURL(baseURL, endpoint), bytes.NewReader(body))
	if err != nil {
		return session{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("apikey", apiKey)
	response, err := (&http.Client{Timeout: 15 * time.Second}).Do(request)
	if err != nil {
		return session{}, err
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 65536))
	if err != nil {
		return session{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return session{}, responseError(response.StatusCode, responseBody)
	}
	values := map[string]json.RawMessage{}
	if err := json.Unmarshal(responseBody, &values); err != nil {
		return session{}, fmt.Errorf("解析 Supabase 会话响应失败：%w", err)
	}
	result := session{AccessToken: jsonString(values, "access_token"), RefreshToken: jsonString(values, "refresh_token")}
	if result.AccessToken == "" || result.RefreshToken == "" {
		return session{}, fmt.Errorf("Supabase 响应未包含 access_token 或 refresh_token")
	}
	return result, nil
}

func validateAccessToken(baseURL, apiKey, accessToken string) (bool, error) {
	request, err := http.NewRequest(http.MethodGet, authURL(baseURL, "user"), nil)
	if err != nil {
		return false, err
	}
	request.Header.Set("apikey", apiKey)
	request.Header.Set("Authorization", "Bearer "+accessToken)
	response, err := (&http.Client{Timeout: 15 * time.Second}).Do(request)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return true, nil
	}
	if response.StatusCode >= 400 && response.StatusCode < 500 {
		return false, nil
	}
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	return false, responseError(response.StatusCode, body)
}

func responseError(status int, body []byte) error {
	values := map[string]json.RawMessage{}
	_ = json.Unmarshal(body, &values)
	message := firstNonEmpty(jsonString(values, "message"), jsonString(values, "error"), strings.TrimSpace(string(body)))
	return fmt.Errorf("Supabase 返回 HTTP %d: %s", status, message)
}

func jsonString(values map[string]json.RawMessage, key string) string {
	var value string
	_ = json.Unmarshal(values[key], &value)
	return value
}

func authURL(baseURL, endpoint string) string {
	return strings.TrimRight(strings.TrimSpace(baseURL), "/") + "/auth/v1/" + endpoint
}
func printSession(w io.Writer, value session) {
	fmt.Fprintf(w, "当前 token: %s\n当前 refreshtoken: %s\n", value.AccessToken, value.RefreshToken)
}

func readPassword(tty *os.File, reader *bufio.Reader) (string, error) {
	if err := runStty(tty, "-echo"); err != nil {
		return "", err
	}
	defer runStty(tty, "echo")
	value, err := reader.ReadString(10)
	return strings.TrimSpace(value), err
}

func makeRaw(tty *os.File) (func(), error) {
	saved, err := sttyOutput(tty, "-g")
	if err != nil {
		return nil, err
	}
	saved = strings.TrimSpace(saved)
	if err := runStty(tty, "raw", "-echo"); err != nil {
		return nil, err
	}
	return func() { _ = runStty(tty, saved) }, nil
}
func runStty(tty *os.File, args ...string) error {
	return exec.Command("stty", append([]string{"-F", tty.Name()}, args...)...).Run()
}
func sttyOutput(tty *os.File, args ...string) (string, error) {
	output, err := exec.Command("stty", append([]string{"-F", tty.Name()}, args...)...).Output()
	return string(output), err
}
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
