//go:build !js

package main

// uploadVertices copies the scratch vertex slice into the pre-allocated
// vertex buffer via queue.WriteBuffer. The buffer was created at startup
// with CopyDst usage, so this is one DMA-style upload per frame.
func (r *renderer) uploadVertices(vertices []quadVertex) {
	r.queue.WriteBuffer(r.vertexBuffer, 0, vertexBytes(vertices))
}

// uploadProjection refreshes the ortho matrix uniform. Called from resize.
func (r *renderer) uploadProjection() {
	r.queue.WriteBuffer(r.uniformBuffer, 0, r.projection.bytes())
}
