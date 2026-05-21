//go:build js

package main

import (
	"syscall/js"
	"time"

	"github.com/cogentcore/webgpu/wgpu"

	"github.com/matthewjberger/freecs-go"
)

func main() {
	doc := js.Global().Get("document")
	canvas := doc.Call("getElementById", "canvas")
	if !canvas.Truthy() {
		js.Global().Get("console").Call("error", "no <canvas id=\"canvas\"> found")
		return
	}

	width := uint32(canvas.Get("width").Int())
	height := uint32(canvas.Get("height").Int())

	instance := wgpu.CreateInstance(nil)
	if instance == nil {
		js.Global().Get("console").Call("error", "WebGPU not supported in this browser")
		return
	}

	surface := instance.CreateSurface(&wgpu.SurfaceDescriptor{Canvas: canvas})

	r, err := newRenderer(instance, surface, width, height)
	if err != nil {
		js.Global().Get("console").Call("error", "renderer init failed: "+err.Error())
		return
	}

	world := freecs.New()
	m := registerComponents(world)
	freecs.SetResource(world, DeltaTime(0))
	freecs.SetResource(world, Input{})
	freecs.SetResource(world, GameState{Lives: startingLives})
	spawnLevel(world, m)

	schedule := freecs.NewSchedule()
	schedule.Push("input", inputSystem)
	schedule.Push("ball-physics", ballPhysicsSystem)
	schedule.Push("paddle-bounce", paddleBounceSystem)
	schedule.Push("brick-collision", brickCollisionSystem())
	schedule.Push("life", lifeSystem)
	schedule.Push("render", renderSystem(r))

	resizeObserver := js.Global().Get("ResizeObserver").New(js.FuncOf(func(_ js.Value, args []js.Value) any {
		entries := args[0]
		if entries.Length() == 0 {
			return nil
		}
		entry := entries.Index(0)
		contentBoxSize := entry.Get("contentBoxSize")
		if !contentBoxSize.Truthy() || contentBoxSize.Length() == 0 {
			return nil
		}
		box := contentBoxSize.Index(0)
		w := uint32(box.Get("inlineSize").Int())
		h := uint32(box.Get("blockSize").Int())
		if w == 0 || h == 0 {
			return nil
		}
		canvas.Set("width", w)
		canvas.Set("height", h)
		r.resize(w, h)
		return nil
	}))
	resizeObserver.Call("observe", canvas)

	keyHandler := func(_ js.Value, args []js.Value) any {
		event := args[0]
		key := event.Get("key").String()
		held := event.Get("type").String() == "keydown"
		input := freecs.MustResource[Input](world)
		state := freecs.MustResource[GameState](world)
		switch key {
		case "ArrowLeft", "a", "A":
			input.Left = held
			event.Call("preventDefault")
		case "ArrowRight", "d", "D":
			input.Right = held
			event.Call("preventDefault")
		case " ", "Spacebar":
			input.Launch = held
			event.Call("preventDefault")
		case "r", "R":
			if held && (state.Won || state.Lost) {
				restart(world, m)
			}
		}
		return nil
	}
	doc.Call("addEventListener", "keydown", js.FuncOf(keyHandler))
	doc.Call("addEventListener", "keyup", js.FuncOf(keyHandler))

	last := time.Now()
	var frame js.Func
	frame = js.FuncOf(func(_ js.Value, _ []js.Value) any {
		now := time.Now()
		dt := float32(now.Sub(last).Seconds())
		last = now
		if dt > 0.05 {
			dt = 0.05
		}
		*freecs.MustResource[DeltaTime](world) = DeltaTime(dt)

		schedule.Run(world)
		world.ApplyCommands()
		world.Step()

		clear := [4]float32{0.05, 0.06, 0.10, 1}
		if err := r.render(clear); err != nil {
			js.Global().Get("console").Call("error", "render error: "+err.Error())
		}

		js.Global().Call("requestAnimationFrame", frame)
		return nil
	})
	js.Global().Call("requestAnimationFrame", frame)

	select {}
}
