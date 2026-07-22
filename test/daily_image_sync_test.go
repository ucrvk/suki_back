package main

import (
	"testing"
	"time"

	"suki_back/lib"
)

func TestTimeUntilNextDailyImageSync(t *testing.T) {
	tests := []struct {
		name string
		now  time.Time
		want time.Duration
	}{
		{
			name: "one second before midnight UTC+8",
			now:  time.Date(2026, time.July, 22, 15, 59, 59, 0, time.UTC),
			want: time.Second,
		},
		{
			name: "at midnight UTC+8 schedules tomorrow",
			now:  time.Date(2026, time.July, 22, 16, 0, 0, 0, time.UTC),
			want: 24 * time.Hour,
		},
		{
			name: "midday UTC+8",
			now:  time.Date(2026, time.July, 22, 4, 30, 0, 0, time.UTC),
			want: 11*time.Hour + 30*time.Minute,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := lib.TimeUntilNextDailyImageSync(tt.now); got != tt.want {
				t.Fatalf("TimeUntilNextDailyImageSync() = %s, want %s", got, tt.want)
			}
		})
	}
}
