package main

import "github.com/egoist/mygo"

// initialWindowBounds 在屏幕容得下时采用1200×800；不足的维度缩至工作区90%，并居中。
func initialWindowBounds(area mygo.Rectangle) mygo.Rectangle {
	width, height := 1200, 800
	if area.Width < width {
		width = area.Width * 9 / 10
	}
	if area.Height < height {
		height = area.Height * 9 / 10
	}
	return mygo.Rectangle{X: area.X + (area.Width-width)/2, Y: area.Y + (area.Height-height)/2, Width: width, Height: height}
}

// fitWindowBounds 保留用户尺寸和位置，仅缩小超出工作区的尺寸并移回越界部分。
func fitWindowBounds(bounds, area mygo.Rectangle) mygo.Rectangle {
	bounds.Width = min(bounds.Width, area.Width)
	bounds.Height = min(bounds.Height, area.Height)
	bounds.X = max(area.X, min(bounds.X, area.X+area.Width-bounds.Width))
	bounds.Y = max(area.Y, min(bounds.Y, area.Y+area.Height-bounds.Height))
	return bounds
}
