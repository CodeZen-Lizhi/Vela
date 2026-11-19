package main

import (
	"testing"

	"github.com/egoist/mygo"
)

// TestWindowBounds 覆盖首次尺寸、小屏缩小、保留用户尺寸及换屏后完整可见。
func TestWindowBounds(t *testing.T) {
	area := mygo.Rectangle{X: 100, Y: 50, Width: 1200, Height: 800}
	if got := initialWindowBounds(area); got != area {
		t.Fatalf("屏幕放得下时不应缩小: %+v", got)
	}
	small := initialWindowBounds(mygo.Rectangle{Width: 1000, Height: 700})
	if small.Width != 900 || small.Height != 630 {
		t.Fatalf("小屏默认尺寸: %+v", small)
	}
	user := mygo.Rectangle{X: 120, Y: 80, Width: 1049, Height: 633}
	if got := fitWindowBounds(user, area); got != user {
		t.Fatalf("用户尺寸应保留: %+v", got)
	}
	got := fitWindowBounds(mygo.Rectangle{X: 2000, Y: 1500, Width: 1500, Height: 900}, area)
	if got != area {
		t.Fatalf("越界窗口未完整收回工作区: %+v", got)
	}
}
