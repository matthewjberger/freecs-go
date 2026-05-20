package main

import (
	"github.com/matthewjberger/freecs-go"
)

// World layout: (0, 0) is top-left. Y grows downward. All sizes are in
// pixels and the projection matrix in render.go maps these to NDC.
const (
	fieldWidth  = 1280
	fieldHeight = 720

	paddleWidth      = 140
	paddleHeight     = 16
	paddleY          = fieldHeight - 60
	paddleStartX     = (fieldWidth - paddleWidth) / 2
	paddleSpeed      = 720
	paddleBallOffset = paddleHeight / 2

	ballSize       = 14
	ballStartSpeed = 480

	playAreaTop = 64

	brickRows    = 6
	brickColumns = 12
	brickWidth   = 88
	brickHeight  = 24
	brickGap     = 6
	brickTop     = playAreaTop + 24
	brickLeft    = (fieldWidth - (brickColumns*brickWidth + (brickColumns-1)*brickGap)) / 2

	startingLives = 3
)

// Components live as plain Go structs. Component types must be Register'ed
// with the world before they can be Spawn'd, Set, or queried.

type Position struct{ X, Y float32 }
type Size struct{ W, H float32 }
type Velocity struct{ X, Y float32 }
type Color struct{ R, G, B, A float32 }

// Brick carries the per-tile score payload. Brick is a regular component,
// so queries reach the brick rows by archetype walk over POSITION | SIZE |
// BRICK, not by a separate tag set.
type Brick struct{ Points int32 }

// Marker types used purely as tags (see freecs.AddTag[T] in game.go's
// spawn helpers).
type Ball struct{}
type Paddle struct{}

// Resources are world-scoped values keyed by type. Define named types so
// freecs.Resource[T] can distinguish them.

type DeltaTime float32

type Input struct {
	Left, Right, Launch bool
}

type GameState struct {
	Lives      int
	Score      int
	Started    bool
	Won        bool
	Lost       bool
	ResetBall  bool
	Paddle     freecs.Entity
	Ball       freecs.Entity
	BrickCount int
}

// masks captures the freecs Mask bits returned at registration. Storing
// them in one struct avoids repeating Register/MaskOf calls in every system.
type masks struct {
	position freecs.Mask
	size     freecs.Mask
	velocity freecs.Mask
	color    freecs.Mask
	brick    freecs.Mask
}

func registerComponents(world *freecs.World) masks {
	return masks{
		position: freecs.Register[Position](world),
		size:     freecs.Register[Size](world),
		velocity: freecs.Register[Velocity](world),
		color:    freecs.Register[Color](world),
		brick:    freecs.Register[Brick](world),
	}
}

// spawnLevel populates the world with paddle, ball, and a brick grid, and
// stores their handles on the GameState resource for later reference.
func spawnLevel(world *freecs.World, m masks) {
	state := freecs.Resource[GameState](world)

	paddle := world.Spawn(m.position | m.size | m.color)
	freecs.Set(world, paddle, Position{X: paddleStartX, Y: paddleY})
	freecs.Set(world, paddle, Size{W: paddleWidth, H: paddleHeight})
	freecs.Set(world, paddle, Color{R: 0.85, G: 0.85, B: 0.95, A: 1})
	freecs.AddTag[Paddle](world, paddle)
	state.Paddle = paddle

	ball := world.Spawn(m.position | m.size | m.velocity | m.color)
	freecs.Set(world, ball, Size{W: ballSize, H: ballSize})
	freecs.Set(world, ball, Color{R: 0.95, G: 0.55, B: 0.25, A: 1})
	freecs.Set(world, ball, Velocity{X: 0, Y: 0})
	freecs.AddTag[Ball](world, ball)
	state.Ball = ball

	resetBallOntoPaddle(world, state)

	palette := [brickRows][4]float32{
		{0.95, 0.30, 0.30, 1},
		{0.95, 0.55, 0.20, 1},
		{0.90, 0.85, 0.20, 1},
		{0.40, 0.80, 0.45, 1},
		{0.30, 0.60, 0.95, 1},
		{0.65, 0.40, 0.95, 1},
	}

	state.BrickCount = 0
	for row := 0; row < brickRows; row++ {
		points := int32((brickRows - row) * 10)
		for col := 0; col < brickColumns; col++ {
			x := float32(brickLeft + col*(brickWidth+brickGap))
			y := float32(brickTop + row*(brickHeight+brickGap))
			brick := world.Spawn(m.position | m.size | m.color | m.brick)
			freecs.Set(world, brick, Position{X: x, Y: y})
			freecs.Set(world, brick, Size{W: brickWidth, H: brickHeight})
			freecs.Set(world, brick, Color{R: palette[row][0], G: palette[row][1], B: palette[row][2], A: 1})
			freecs.Set(world, brick, Brick{Points: points})
			state.BrickCount++
		}
	}
}

func resetBallOntoPaddle(world *freecs.World, state *GameState) {
	state.Started = false
	paddlePos, _ := freecs.Get[Position](world, state.Paddle)
	freecs.Set(world, state.Ball, Position{
		X: paddlePos.X + paddleWidth/2 - ballSize/2,
		Y: paddleY - ballSize - paddleBallOffset,
	})
	freecs.Set(world, state.Ball, Velocity{X: 0, Y: 0})
}

// --- systems ---

func inputSystem(world *freecs.World) {
	input := freecs.Resource[Input](world)
	state := freecs.Resource[GameState](world)
	delta := float32(*freecs.Resource[DeltaTime](world))

	paddlePos, _ := freecs.GetMut[Position](world, state.Paddle)
	if paddlePos == nil {
		return
	}
	if input.Left {
		paddlePos.X -= paddleSpeed * delta
	}
	if input.Right {
		paddlePos.X += paddleSpeed * delta
	}
	if paddlePos.X < 0 {
		paddlePos.X = 0
	}
	if paddlePos.X > fieldWidth-paddleWidth {
		paddlePos.X = fieldWidth - paddleWidth
	}

	if input.Launch && !state.Started && !state.Won && !state.Lost {
		state.Started = true
		freecs.Set(world, state.Ball, Velocity{X: ballStartSpeed * 0.5, Y: -ballStartSpeed})
	}

	if !state.Started {
		freecs.Set(world, state.Ball, Position{
			X: paddlePos.X + paddleWidth/2 - ballSize/2,
			Y: paddleY - ballSize - paddleBallOffset,
		})
	}
}

func ballPhysicsSystem(world *freecs.World) {
	state := freecs.Resource[GameState](world)
	if !state.Started {
		return
	}
	delta := float32(*freecs.Resource[DeltaTime](world))

	position, _ := freecs.GetMut[Position](world, state.Ball)
	velocity, _ := freecs.GetMut[Velocity](world, state.Ball)
	if position == nil || velocity == nil {
		return
	}
	position.X += velocity.X * delta
	position.Y += velocity.Y * delta

	if position.X < 0 {
		position.X = 0
		velocity.X = -velocity.X
	}
	if position.X+ballSize > fieldWidth {
		position.X = fieldWidth - ballSize
		velocity.X = -velocity.X
	}
	if position.Y < playAreaTop {
		position.Y = playAreaTop
		velocity.Y = -velocity.Y
	}
}

func paddleBounceSystem(world *freecs.World) {
	state := freecs.Resource[GameState](world)
	if !state.Started {
		return
	}

	ballPosition, _ := freecs.GetMut[Position](world, state.Ball)
	ballVelocity, _ := freecs.GetMut[Velocity](world, state.Ball)
	paddlePosition, _ := freecs.Get[Position](world, state.Paddle)
	if ballPosition == nil || ballVelocity == nil || paddlePosition == nil {
		return
	}

	if !aabbOverlap(ballPosition.X, ballPosition.Y, ballSize, ballSize,
		paddlePosition.X, paddlePosition.Y, paddleWidth, paddleHeight) {
		return
	}
	if ballVelocity.Y <= 0 {
		return
	}

	ballCenterX := ballPosition.X + ballSize/2
	paddleCenterX := paddlePosition.X + paddleWidth/2
	offset := (ballCenterX - paddleCenterX) / (paddleWidth / 2)
	if offset < -1 {
		offset = -1
	}
	if offset > 1 {
		offset = 1
	}
	const maxBounceAngle = 0.85
	ballVelocity.X = ballStartSpeed * offset * maxBounceAngle
	ballVelocity.Y = -ballStartSpeed
	ballPosition.Y = paddlePosition.Y - ballSize - 1
}

func brickCollisionSystem() freecs.SystemFn {
	return func(world *freecs.World) {
		state := freecs.Resource[GameState](world)
		if !state.Started {
			return
		}
		ballPosition, _ := freecs.GetMut[Position](world, state.Ball)
		ballVelocity, _ := freecs.GetMut[Velocity](world, state.Ball)
		if ballPosition == nil || ballVelocity == nil {
			return
		}

		ballX := ballPosition.X
		ballY := ballPosition.Y

		freecs.Iter3[Position, Size, Brick](world, 0, 0, func(entity freecs.Entity, position *Position, size *Size, brick *Brick) {
			if !aabbOverlap(ballX, ballY, ballSize, ballSize, position.X, position.Y, size.W, size.H) {
				return
			}

			overlapLeft := (ballX + ballSize) - position.X
			overlapRight := (position.X + size.W) - ballX
			overlapTop := (ballY + ballSize) - position.Y
			overlapBottom := (position.Y + size.H) - ballY

			minOverlap := overlapLeft
			axis := "x-"
			if overlapRight < minOverlap {
				minOverlap = overlapRight
				axis = "x+"
			}
			if overlapTop < minOverlap {
				minOverlap = overlapTop
				axis = "y-"
			}
			if overlapBottom < minOverlap {
				minOverlap = overlapBottom
				axis = "y+"
			}

			switch axis {
			case "x-":
				if ballVelocity.X > 0 {
					ballVelocity.X = -ballVelocity.X
				}
				ballPosition.X = position.X - ballSize - 1
			case "x+":
				if ballVelocity.X < 0 {
					ballVelocity.X = -ballVelocity.X
				}
				ballPosition.X = position.X + size.W + 1
			case "y-":
				if ballVelocity.Y > 0 {
					ballVelocity.Y = -ballVelocity.Y
				}
				ballPosition.Y = position.Y - ballSize - 1
			case "y+":
				if ballVelocity.Y < 0 {
					ballVelocity.Y = -ballVelocity.Y
				}
				ballPosition.Y = position.Y + size.H + 1
			}

			state.Score += int(brick.Points)
			world.QueueDespawn(entity)
			state.BrickCount--
			// One brick per frame keeps the bounce response readable; bricks
			// stacked behind this one will be picked up next frame.
			ballX = ballPosition.X
			ballY = ballPosition.Y
		})

		if state.BrickCount <= 0 && !state.Won {
			state.Won = true
		}
	}
}

func lifeSystem(world *freecs.World) {
	state := freecs.Resource[GameState](world)
	if !state.Started || state.Lost || state.Won {
		return
	}
	position, _ := freecs.Get[Position](world, state.Ball)
	if position == nil {
		return
	}
	if position.Y < fieldHeight {
		return
	}
	state.Lives--
	if state.Lives <= 0 {
		state.Lost = true
	}
	resetBallOntoPaddle(world, state)
}

func renderSystem(r *renderer) freecs.SystemFn {
	return func(world *freecs.World) {
		state := freecs.Resource[GameState](world)
		r.beginFrame()

		freecs.Iter3[Position, Size, Color](world, 0, 0, func(_ freecs.Entity, position *Position, size *Size, color *Color) {
			r.pushQuad(position.X, position.Y, size.W, size.H, [4]float32{color.R, color.G, color.B, color.A})
		})

		drawHud(r, state)
	}
}

// drawHud paints status bars at the top of the screen using simple solid
// rectangles. The HUD lives above the playArea so the ball never overlaps it.
// Lives are pips on the left; the progress bar fills the strip across the top
// in proportion to bricks remaining.
func drawHud(r *renderer, state *GameState) {
	r.pushQuad(0, 0, fieldWidth, playAreaTop, [4]float32{0.09, 0.11, 0.18, 1})
	r.pushQuad(0, playAreaTop-2, fieldWidth, 2, [4]float32{0.20, 0.24, 0.34, 1})

	for index := 0; index < state.Lives; index++ {
		x := float32(20 + index*22)
		r.pushQuad(x, 16, 16, 16, [4]float32{0.95, 0.55, 0.25, 1})
	}

	totalBricks := float32(brickRows * brickColumns)
	remaining := float32(state.BrickCount)
	if remaining < 0 {
		remaining = 0
	}
	progress := 1.0 - remaining/totalBricks
	barWidth := float32(fieldWidth - 320)
	barX := float32(280)
	r.pushQuad(barX, 24, barWidth, 8, [4]float32{0.18, 0.20, 0.30, 1})
	r.pushQuad(barX, 24, barWidth*progress, 8, [4]float32{0.55, 0.80, 0.40, 1})

	if !state.Started {
		r.pushQuad(fieldWidth/2-90, fieldHeight/2+60, 180, 6, [4]float32{0.7, 0.7, 0.85, 0.8})
	}
	if state.Won {
		r.pushQuad(0, 0, fieldWidth, fieldHeight, [4]float32{0.25, 0.60, 0.35, 0.4})
	}
	if state.Lost {
		r.pushQuad(0, 0, fieldWidth, fieldHeight, [4]float32{0.65, 0.20, 0.25, 0.4})
	}
}

func aabbOverlap(ax, ay, aw, ah, bx, by, bw, bh float32) bool {
	return ax < bx+bw && ax+aw > bx && ay < by+bh && ay+ah > by
}

// restart tears down every entity and rebuilds the level. Used when the
// player presses R after winning or losing.
func restart(world *freecs.World, m masks) {
	for entity := range world.Query(m.position, 0) {
		world.QueueDespawn(entity)
	}
	world.ApplyCommands()
	*freecs.Resource[GameState](world) = GameState{Lives: startingLives}
	spawnLevel(world, m)
}
