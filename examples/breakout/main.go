//go:build !js

package main

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/cogentcore/webgpu/wgpu"
	"github.com/cogentcore/webgpu/wgpuglfw"
	"github.com/go-gl/glfw/v3.3/glfw"

	"github.com/matthewjberger/freecs-go"
)

func init() {
	runtime.LockOSThread()

	switch os.Getenv("WGPU_LOG_LEVEL") {
	case "OFF":
		wgpu.SetLogLevel(wgpu.LogLevelOff)
	case "ERROR":
		wgpu.SetLogLevel(wgpu.LogLevelError)
	case "WARN":
		wgpu.SetLogLevel(wgpu.LogLevelWarn)
	case "INFO":
		wgpu.SetLogLevel(wgpu.LogLevelInfo)
	case "DEBUG":
		wgpu.SetLogLevel(wgpu.LogLevelDebug)
	case "TRACE":
		wgpu.SetLogLevel(wgpu.LogLevelTrace)
	}
}

func main() {
	if err := glfw.Init(); err != nil {
		panic(err)
	}
	defer glfw.Terminate()

	glfw.WindowHint(glfw.ClientAPI, glfw.NoAPI)
	window, err := glfw.CreateWindow(fieldWidth, fieldHeight, "Breakout (freecs-go + wgpu)", nil, nil)
	if err != nil {
		panic(err)
	}
	defer window.Destroy()

	instance := wgpu.CreateInstance(nil)
	defer instance.Release()

	surface := instance.CreateSurface(wgpuglfw.GetSurfaceDescriptor(window))

	width, height := window.GetSize()
	r, err := newRenderer(instance, surface, uint32(width), uint32(height))
	if err != nil {
		panic(err)
	}
	defer r.release()

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

	window.SetSizeCallback(func(_ *glfw.Window, w, h int) {
		if w > 0 && h > 0 {
			r.resize(uint32(w), uint32(h))
		}
	})

	window.SetKeyCallback(func(w *glfw.Window, key glfw.Key, _ int, action glfw.Action, _ glfw.ModifierKey) {
		if key == glfw.KeyEscape && action == glfw.Press {
			w.SetShouldClose(true)
			return
		}
		input := freecs.Resource[Input](world)
		state := freecs.Resource[GameState](world)
		switch action {
		case glfw.Press, glfw.Repeat:
			switch key {
			case glfw.KeyLeft, glfw.KeyA:
				input.Left = true
			case glfw.KeyRight, glfw.KeyD:
				input.Right = true
			case glfw.KeySpace:
				input.Launch = true
			case glfw.KeyR:
				if state.Won || state.Lost {
					restart(world, m)
				}
			}
		case glfw.Release:
			switch key {
			case glfw.KeyLeft, glfw.KeyA:
				input.Left = false
			case glfw.KeyRight, glfw.KeyD:
				input.Right = false
			case glfw.KeySpace:
				input.Launch = false
			}
		}
	})

	last := time.Now()
	for !window.ShouldClose() {
		glfw.PollEvents()

		now := time.Now()
		dt := float32(now.Sub(last).Seconds())
		last = now
		if dt > 0.05 {
			dt = 0.05
		}
		*freecs.Resource[DeltaTime](world) = DeltaTime(dt)

		schedule.Run(world)
		world.ApplyCommands()
		world.Step()

		clear := [4]float32{0.05, 0.06, 0.10, 1}
		if err := r.render(clear); err != nil {
			errstr := err.Error()
			switch {
			case strings.Contains(errstr, "Surface timed out"),
				strings.Contains(errstr, "Surface is outdated"),
				strings.Contains(errstr, "Surface was lost"),
				strings.Contains(errstr, "Outdated"):
				r.reconfigure()
			default:
				panic(err)
			}
		}

		state := freecs.Resource[GameState](world)
		title := fmt.Sprintf("Breakout — score %d   lives %d", state.Score, state.Lives)
		if state.Won {
			title += "   (you win, press R)"
		} else if state.Lost {
			title += "   (game over, press R)"
		} else if !state.Started {
			title += "   (space to launch)"
		}
		window.SetTitle(title)
	}
}
