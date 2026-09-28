import { readFileSync } from 'fs'
import { resolve } from 'path'
import { describe, expect, it } from 'vitest'
import { Mesh } from 'three'
import { MeshoptDecoder } from 'three/examples/jsm/libs/meshopt_decoder.module.js'

import { $loader, CachingGLTFLoader } from './CachingGLTFLoader'
import { ModelViewerGLTFInstance } from './gltf-instance/ModelViewerGLTFInstance'

// Author-time provenance: gltf-transform weld+quantize+meshopt applied to
// the artifacts/walk-test-room.glb regression model, so the fixture is the
// small twin of wanke_weld_meshopt.glb. All 24 meshes decode to exactly 24
// position vertices (GLB JSON chunk + gltf-transform inspect, recorded at
// authoring time); the assertion pins the largest so any mesh decoding to
// a wrong count fails, not just the meshes that happen to match.
const fixture = readFileSync(resolve(__dirname, 'testdata/room_meshopt.glb'))
const fixtureLargestMeshVertexCount = 24

describe('CachingGLTFLoader meshopt wiring', () => {
  it('wires the bundled decoder into every loader', () => {
    const caching = new CachingGLTFLoader(ModelViewerGLTFInstance)

    expect((caching as any)[$loader].meshoptDecoder).toBe(MeshoptDecoder)
    expect(MeshoptDecoder.supported).toBe(true)
  })

  it('defaults draco and KTX2 decoder locations to the local origin', () => {
    // Module init must set the statics before any element or load exists:
    // loaders used directly (as above) fetch decoders too.
    expect(CachingGLTFLoader.getDRACODecoderLocation()).toBe('/assets/draco/')
    expect(CachingGLTFLoader.getKTX2TranscoderLocation()).toBe('/assets/basis/')

    // The loader singletons the GLTF parser actually consults carry the
    // same paths — this is what a draco/KTX2 model fetches at runtime.
    const caching = new CachingGLTFLoader(ModelViewerGLTFInstance)
    const loader = (caching as any)[$loader]
    expect(loader.dracoLoader.decoderPath).toBe('/assets/draco/')
    expect(loader.ktx2Loader.transcoderPath).toBe('/assets/basis/')
  })

  it('parses an EXT_meshopt_compression GLB through the fork\'s loader', async () => {
    const caching = new CachingGLTFLoader(ModelViewerGLTFInstance)
    const loader = (caching as any)[$loader]

    const gltf = await new Promise<any>((res, rej) => {
      // Copy into an exact-size ArrayBuffer: a Node Buffer can be a view
      // with non-zero byteOffset, whose .buffer's first bytes are not 'glTF'.
      loader.parse(new Uint8Array(fixture).buffer, '', res, rej)
    })

    const meshes: Mesh[] = []
    gltf.scene.traverse((o: any) => {
      if (o.isMesh) meshes.push(o)
    })
    const largest = Math.max(...meshes.map((m) => m.geometry.attributes.position.count))
    expect(largest).toBe(fixtureLargestMeshVertexCount)
  })
})
