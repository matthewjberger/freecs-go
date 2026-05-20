//go:build js

package main

import (
	"unsafe"

	"github.com/cogentcore/webgpu/wgpu"
)

// On wasm the cogentcore writeBuffer path builds a typed-array view over
// wasm linear memory via jsx.BytesToJS. When Go grows the wasm heap the
// underlying ArrayBuffer is detached and subsequent constructions throw
// "Cannot perform Construct on a detached ArrayBuffer". CreateBufferInit
// goes through js.CopyBytesToJS, which is safe, so we replace each upload
// with a release-old + create-new pair.

func (r *renderer) uploadVertices(vertices []quadVertex) {
	newBuf, err := r.device.CreateBufferInit(&wgpu.BufferInitDescriptor{
		Label:    "Quad Vertex Buffer",
		Contents: vertexBytes(vertices),
		Usage:    wgpu.BufferUsageVertex,
	})
	if err != nil {
		return
	}
	if r.vertexBuffer != nil {
		r.vertexBuffer.Release()
	}
	r.vertexBuffer = newBuf
}

func (r *renderer) uploadProjection() {
	newBuf, err := r.device.CreateBufferInit(&wgpu.BufferInitDescriptor{
		Label:    "Ortho Uniform",
		Contents: r.projection.bytes(),
		Usage:    wgpu.BufferUsageUniform | wgpu.BufferUsageCopyDst,
	})
	if err != nil {
		return
	}
	newGroup, err := r.device.CreateBindGroup(&wgpu.BindGroupDescriptor{
		Label:  "uniform_bind_group",
		Layout: r.bindGroupLayout,
		Entries: []wgpu.BindGroupEntry{{
			Binding: 0,
			Buffer:  newBuf,
			Offset:  0,
			Size:    uint64(unsafe.Sizeof(r.projection)),
		}},
	})
	if err != nil {
		newBuf.Release()
		return
	}
	if r.bindGroup != nil {
		r.bindGroup.Release()
	}
	if r.uniformBuffer != nil {
		r.uniformBuffer.Release()
	}
	r.uniformBuffer = newBuf
	r.bindGroup = newGroup
}
