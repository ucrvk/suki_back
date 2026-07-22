# Supabase 交互式认证测试

该工具用于手动验证 Supabase 密码登录、access token 有效性检测，以及 refresh token 刷新会话的行为。

## 运行前配置

设置 Supabase 项目地址与 anon/public API key。工具优先读取 MAIDPIC_SUPABASE_URL 和 MAIDPIC_SUPABASE_API_KEY，也兼容 SUPABASE_URL 和 SUPABASE_API_KEY。

示例：

    export MAIDPIC_SUPABASE_URL=https://your-project.supabase.co
    export MAIDPIC_SUPABASE_API_KEY=your-anon-or-publishable-key

## 启动

    go run ./test/supabase_auth_cli.go

程序会要求输入 Supabase 用户名（通常为邮箱）和密码；密码输入不会回显。

## 按键操作

| 按键 | 操作 |
| --- | --- |
| / | 调用 /auth/v1/user 检测当前 access token 是否有效。 |
| . | 使用当前 refresh token 获取新的 access token 与 refresh token。 |
| q 或 Ctrl-C | 退出测试程序。 |

每次按下 / 或 . 后，程序都会打印当前完整的 token 和 refreshtoken。Token 仅保存在进程内存，程序不会写入数据库或本地文件。

## 安全提醒

该工具按需求打印完整 token。请仅在受信任的本地终端使用，避免在共享屏幕、录屏、CI 日志或其他会持久化控制台输出的环境中运行。

此工具通过 Linux 的 stty 实现不需要回车的单键读取，因此适用于当前 Linux 开发环境。
