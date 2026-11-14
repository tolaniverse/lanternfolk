// Package worldspike is a deliberately small Asuka feasibility probe. It
// draws a tile grid with native Canvas and pans it with a native gesture; it
// does not claim pixel-perfect scaling or cross-platform input parity yet.
package worldspike

import (
	"math"

	"github.com/egoist/asuka/colors"
	"github.com/egoist/asuka/signal"
	"github.com/egoist/asuka/ui"
)

const tile = float32(24)

// Camera holds the world-space offset in screen points.
type Camera struct{ X, Y float32 }

// Pan applies a gesture's total translation without double-counting repeated
// Changed events. At Ended/Cancelled, a new gesture starts from the final
// camera position.
func Pan(base Camera, translationX, translationY float32) Camera {
	return Camera{X: base.X + translationX, Y: base.Y + translationY}
}

// World renders a bounded grid with one lantern marker. A pan gesture moves
// the camera. Asuka's Canvas uses native drawing commands on each host.
func World() ui.Node {
	camera := signal.New(Camera{})
	base := Camera{}
	canvas := ui.Canvas(func(g *ui.Graphics) {
		w, h := g.Size()
		g.Fill(g.Path().Rect(0, 0, w, h), colors.Hex(0x1b1a24))
		c := camera.Get()
		// Draw only visible cells; avoid an unbounded command buffer.
		minX := int(math.Floor(float64(-c.X/tile))) - 1
		maxX := int(math.Ceil(float64((w-c.X)/tile))) + 1
		minY := int(math.Floor(float64(-c.Y/tile))) - 1
		maxY := int(math.Ceil(float64((h-c.Y)/tile))) + 1
		for y := max(minY, -16); y <= min(maxY, 16); y++ {
			for x := max(minX, -16); x <= min(maxX, 16); x++ {
				shade := uint32(0x343044)
				if (x+y)&1 == 0 {
					shade = 0x3d3750
				}
				g.Fill(g.Path().Rect(c.X+float32(x)*tile, c.Y+float32(y)*tile, tile-1, tile-1), colors.Hex(shade))
			}
		}
		g.Fill(g.Path().Rect(c.X+9*tile, c.Y+7*tile, 12, 18), colors.Hex(0xf2c66d))
	}, ui.Style{Grow: 1})
	return ui.View(ui.ViewProps{Style: ui.Style{Grow: 1}, OnPan: func(g ui.Gesture) {
		switch g.State {
		case ui.GestureBegan:
			base = camera.Peek()
		case ui.GestureChanged:
			camera.Set(Pan(base, g.TranslationX, g.TranslationY))
		case ui.GestureEnded:
			camera.Set(Pan(base, g.TranslationX, g.TranslationY))
		case ui.GestureCancelled:
			camera.Set(base)
		}
	}}, canvas)
}
