package main

import (
	_ "embed"
	"unsafe"

	"github.com/cogentcore/webgpu/wgpu"
)

//go:embed shader.wgsl
var shaderSource string

// quadVertex matches the WGSL VertexInput. Triangle-list, no index buffer:
// each quad is six vertices appended in order.
type quadVertex struct {
	position [2]float32
	color    [4]float32
}

// maxQuadsPerFrame caps the per-frame vertex buffer. Breakout uses at most
// ~70 quads (paddle + ball + 60 bricks + a few status quads) so 256 leaves
// headroom for cosmetic additions without growing the buffer.
const maxQuadsPerFrame = 256
const verticesPerQuad = 6

type orthoUniform struct {
	projection [16]float32
}

func (u *orthoUniform) bytes() []byte {
	return unsafe.Slice((*byte)(unsafe.Pointer(u)), unsafe.Sizeof(*u))
}

// renderer owns the wgpu surface, pipeline, and the CPU-side scratch buffer
// that game systems fill each frame. Render flushes the scratch into the
// vertex buffer, then issues one draw call.
type renderer struct {
	surface       *wgpu.Surface
	adapter       *wgpu.Adapter
	device        *wgpu.Device
	queue         *wgpu.Queue
	surfaceConfig *wgpu.SurfaceConfiguration
	surfaceFormat wgpu.TextureFormat

	pipeline        *wgpu.RenderPipeline
	uniformBuffer   *wgpu.Buffer
	bindGroup       *wgpu.BindGroup
	bindGroupLayout *wgpu.BindGroupLayout

	vertexBuffer   *wgpu.Buffer
	vertexCapacity int

	scratch    []quadVertex
	quadsThis  int
	projection orthoUniform
}

func newRenderer(instance *wgpu.Instance, surface *wgpu.Surface, width, height uint32) (*renderer, error) {
	adapter, err := instance.RequestAdapter(&wgpu.RequestAdapterOptions{
		CompatibleSurface: surface,
	})
	if err != nil {
		return nil, err
	}
	device, err := adapter.RequestDevice(nil)
	if err != nil {
		return nil, err
	}
	queue := device.GetQueue()

	caps := surface.GetCapabilities(adapter)
	surfaceFormat := caps.Formats[0]
	for _, format := range caps.Formats {
		if !isSrgb(format) {
			surfaceFormat = format
			break
		}
	}

	config := &wgpu.SurfaceConfiguration{
		Usage:       wgpu.TextureUsageRenderAttachment,
		Format:      surfaceFormat,
		Width:       width,
		Height:      height,
		PresentMode: caps.PresentModes[0],
		AlphaMode:   caps.AlphaModes[0],
	}
	surface.Configure(adapter, device, config)

	uniform := orthoUniform{projection: orthoTopLeft(float32(width), float32(height))}
	uniformBuffer, err := device.CreateBufferInit(&wgpu.BufferInitDescriptor{
		Label:    "Ortho Uniform",
		Contents: uniform.bytes(),
		Usage:    wgpu.BufferUsageUniform | wgpu.BufferUsageCopyDst,
	})
	if err != nil {
		return nil, err
	}

	bindGroupLayout, err := device.CreateBindGroupLayout(&wgpu.BindGroupLayoutDescriptor{
		Label: "uniform_bind_group_layout",
		Entries: []wgpu.BindGroupLayoutEntry{{
			Binding:    0,
			Visibility: wgpu.ShaderStageVertex,
			Buffer: wgpu.BufferBindingLayout{
				Type:             wgpu.BufferBindingTypeUniform,
				HasDynamicOffset: false,
				MinBindingSize:   0,
			},
		}},
	})
	if err != nil {
		return nil, err
	}

	bindGroup, err := device.CreateBindGroup(&wgpu.BindGroupDescriptor{
		Label:  "uniform_bind_group",
		Layout: bindGroupLayout,
		Entries: []wgpu.BindGroupEntry{{
			Binding: 0,
			Buffer:  uniformBuffer,
			Offset:  0,
			Size:    uint64(unsafe.Sizeof(uniform)),
		}},
	})
	if err != nil {
		return nil, err
	}

	pipeline, err := createPipeline(device, surfaceFormat, bindGroupLayout)
	if err != nil {
		return nil, err
	}

	vertexCapacity := maxQuadsPerFrame * verticesPerQuad
	emptyVertices := make([]quadVertex, vertexCapacity)
	vertexBuffer, err := device.CreateBufferInit(&wgpu.BufferInitDescriptor{
		Label:    "Quad Vertex Buffer",
		Contents: vertexBytes(emptyVertices),
		Usage:    wgpu.BufferUsageVertex | wgpu.BufferUsageCopyDst,
	})
	if err != nil {
		return nil, err
	}

	return &renderer{
		surface:         surface,
		adapter:         adapter,
		device:          device,
		queue:           queue,
		surfaceConfig:   config,
		surfaceFormat:   surfaceFormat,
		pipeline:        pipeline,
		uniformBuffer:   uniformBuffer,
		bindGroup:       bindGroup,
		bindGroupLayout: bindGroupLayout,
		vertexBuffer:    vertexBuffer,
		vertexCapacity:  vertexCapacity,
		scratch:         make([]quadVertex, 0, vertexCapacity),
		projection:      uniform,
	}, nil
}

func createPipeline(device *wgpu.Device, surfaceFormat wgpu.TextureFormat, layout *wgpu.BindGroupLayout) (*wgpu.RenderPipeline, error) {
	shader, err := device.CreateShaderModule(&wgpu.ShaderModuleDescriptor{
		WGSLDescriptor: &wgpu.ShaderModuleWGSLDescriptor{Code: shaderSource},
	})
	if err != nil {
		return nil, err
	}
	defer shader.Release()

	pipelineLayout, err := device.CreatePipelineLayout(&wgpu.PipelineLayoutDescriptor{
		BindGroupLayouts: []*wgpu.BindGroupLayout{layout},
	})
	if err != nil {
		return nil, err
	}
	defer pipelineLayout.Release()

	return device.CreateRenderPipeline(&wgpu.RenderPipelineDescriptor{
		Layout: pipelineLayout,
		Vertex: wgpu.VertexState{
			Module:     shader,
			EntryPoint: "vertex_main",
			Buffers: []wgpu.VertexBufferLayout{{
				ArrayStride: uint64(unsafe.Sizeof(quadVertex{})),
				StepMode:    wgpu.VertexStepModeVertex,
				Attributes: []wgpu.VertexAttribute{
					{Format: wgpu.VertexFormatFloat32x2, Offset: 0, ShaderLocation: 0},
					{Format: wgpu.VertexFormatFloat32x4, Offset: 8, ShaderLocation: 1},
				},
			}},
		},
		Primitive: wgpu.PrimitiveState{
			Topology:  wgpu.PrimitiveTopologyTriangleList,
			FrontFace: wgpu.FrontFaceCCW,
			CullMode:  wgpu.CullModeNone,
		},
		Multisample: wgpu.MultisampleState{
			Count:                  1,
			Mask:                   0xFFFFFFFF,
			AlphaToCoverageEnabled: false,
		},
		Fragment: &wgpu.FragmentState{
			Module:     shader,
			EntryPoint: "fragment_main",
			Targets: []wgpu.ColorTargetState{{
				Format:    surfaceFormat,
				Blend:     &wgpu.BlendStateAlphaBlending,
				WriteMask: wgpu.ColorWriteMaskAll,
			}},
		},
	})
}

// resize re-configures the surface and rebuilds the ortho projection so quad
// world coordinates remain in screen pixels regardless of window size.
func (r *renderer) resize(width, height uint32) {
	r.surfaceConfig.Width = width
	r.surfaceConfig.Height = height
	r.surface.Configure(r.adapter, r.device, r.surfaceConfig)
	r.projection.projection = orthoTopLeft(float32(width), float32(height))
	r.uploadProjection()
}

func (r *renderer) reconfigure() {
	r.surface.Configure(r.adapter, r.device, r.surfaceConfig)
}

// beginFrame clears the scratch buffer so systems can append quads with
// pushQuad for this frame.
func (r *renderer) beginFrame() {
	r.scratch = r.scratch[:0]
	r.quadsThis = 0
}

// pushQuad appends one axis-aligned colored rectangle. (x, y) is the
// top-left corner; (w, h) extend right/down in screen pixels.
func (r *renderer) pushQuad(x, y, w, h float32, color [4]float32) {
	if r.quadsThis >= maxQuadsPerFrame {
		return
	}
	r.quadsThis++

	right := x + w
	bottom := y + h
	topLeft := quadVertex{position: [2]float32{x, y}, color: color}
	topRight := quadVertex{position: [2]float32{right, y}, color: color}
	bottomRight := quadVertex{position: [2]float32{right, bottom}, color: color}
	bottomLeft := quadVertex{position: [2]float32{x, bottom}, color: color}

	r.scratch = append(r.scratch,
		topLeft, bottomLeft, bottomRight,
		topLeft, bottomRight, topRight,
	)
}

// render uploads the scratch vertices and submits one draw call.
func (r *renderer) render(clear [4]float32) error {
	if r.quadsThis > 0 {
		r.uploadVertices(r.scratch)
	}

	surfaceTex, err := r.surface.GetCurrentTexture()
	if err != nil {
		return err
	}
	view, err := surfaceTex.CreateView(nil)
	if err != nil {
		return err
	}
	defer view.Release()

	encoder, err := r.device.CreateCommandEncoder(&wgpu.CommandEncoderDescriptor{Label: "Frame Encoder"})
	if err != nil {
		return err
	}
	defer encoder.Release()

	pass := encoder.BeginRenderPass(&wgpu.RenderPassDescriptor{
		Label: "Quad Pass",
		ColorAttachments: []wgpu.RenderPassColorAttachment{{
			View:       view,
			LoadOp:     wgpu.LoadOpClear,
			StoreOp:    wgpu.StoreOpStore,
			ClearValue: wgpu.Color{R: float64(clear[0]), G: float64(clear[1]), B: float64(clear[2]), A: float64(clear[3])},
		}},
	})

	if r.quadsThis > 0 {
		pass.SetPipeline(r.pipeline)
		pass.SetBindGroup(0, r.bindGroup, nil)
		pass.SetVertexBuffer(0, r.vertexBuffer, 0, wgpu.WholeSize)
		pass.Draw(uint32(r.quadsThis*verticesPerQuad), 1, 0, 0)
	}
	pass.End()
	pass.Release()

	cmd, err := encoder.Finish(nil)
	if err != nil {
		return err
	}
	defer cmd.Release()

	r.queue.Submit(cmd)
	r.surface.Present()
	return nil
}

func (r *renderer) release() {
	if r.pipeline != nil {
		r.pipeline.Release()
	}
	if r.bindGroup != nil {
		r.bindGroup.Release()
	}
	if r.bindGroupLayout != nil {
		r.bindGroupLayout.Release()
	}
	if r.uniformBuffer != nil {
		r.uniformBuffer.Release()
	}
	if r.vertexBuffer != nil {
		r.vertexBuffer.Release()
	}
	if r.queue != nil {
		r.queue.Release()
	}
	if r.device != nil {
		r.device.Release()
	}
	if r.adapter != nil {
		r.adapter.Release()
	}
	if r.surface != nil {
		r.surface.Release()
	}
}

// orthoTopLeft returns a column-major projection that maps (0,0) to the
// top-left corner of the viewport and (width, height) to the bottom-right.
// Z is clamped to 0 since we draw 2D quads with no depth test.
func orthoTopLeft(width, height float32) [16]float32 {
	return [16]float32{
		2.0 / width, 0, 0, 0,
		0, -2.0 / height, 0, 0,
		0, 0, 1, 0,
		-1, 1, 0, 1,
	}
}

func vertexBytes(vertices []quadVertex) []byte {
	if len(vertices) == 0 {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(&vertices[0])), len(vertices)*int(unsafe.Sizeof(quadVertex{})))
}

func isSrgb(f wgpu.TextureFormat) bool {
	switch f {
	case wgpu.TextureFormatRGBA8UnormSrgb, wgpu.TextureFormatBGRA8UnormSrgb:
		return true
	}
	return false
}
