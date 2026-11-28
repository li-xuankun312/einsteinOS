// verify_window_kernels.cu - multi-token (speculative verify window) gated-delta-net kernels and small device-side helpers
// for BI-V100: window conv + l2, history commit, alpha/beta, T-token state step + norm with replay and a device-side keep
// count, embedding / blob / row gathers, stream broadcasts, step builders, pointer rebase, window ids, MTP select.
#include <cuda_runtime.h>
#include <algorithm>
#include <cmath>
#include <cstdint>
#include "baloon/verify_window_kernels.hpp"
#include "ilu_gdn_shfl.cuh"

namespace baloon {
namespace vwin {
namespace {

using ilu_gdn::lane_reduce;
constexpr int kBlk = 256;
constexpr int kTop = 512;    // row_top_prob threads: 16 segments of 32 lanes (512 is the widest block used on this device)

inline int check() {
    const cudaError_t e = cudaGetLastError();
    return e == cudaSuccess ? 0 : (int)e;
}
inline unsigned blocks_of(uint64_t n, unsigned per) { return (unsigned)((n + per - 1) / per); }
inline cudaStream_t strm(void* s) { return (cudaStream_t)s; }

// ------------------------------------------------------------------------------------------------ conv window
__global__ void window_conv_kernel(const float* __restrict__ x, const float* __restrict__ hist, const float* __restrict__ w,
                                   float* __restrict__ out, int T, int C) {
    const uint64_t g = (uint64_t)blockIdx.x * blockDim.x + threadIdx.x;
    if (g >= (uint64_t)T * C) return;
    const int t = (int)(g / C), c = (int)(g % C);
    float acc = 0.f;
#pragma unroll
    for (int i = 0; i < 3; ++i) {
        const int e = t + i;   // entry of [hist0..2, x0, ...]
        const float val = e < 3 ? hist[(size_t)e * C + c] : x[(size_t)(e - 3) * C + c];
        acc += val * w[(size_t)c * 4 + i];
    }
    acc += x[(size_t)t * C + c] * w[(size_t)c * 4 + 3];
    out[g] = acc;
}

// grid (2*hk, T), 128 threads: l2-normalise one q/k head of one token.
__global__ void window_l2_kernel(float* __restrict__ out, int C, float eps) {
    __shared__ float part[S / 32];
    const int head = blockIdx.x, t = blockIdx.y, tid = threadIdx.x;
    float* p = out + (size_t)t * C + (size_t)head * S;
    const float v = p[tid];
    float ss = lane_reduce(v * v);
    if ((tid & 31) == 0) part[tid >> 5] = ss;
    __syncthreads();
    ss = ((part[0] + part[1]) + part[2]) + part[3];
    p[tid] = v / fmaxf(sqrtf(ss), eps);
}

__global__ void gdn_conv_commit_kernel(const float* __restrict__ x, float* __restrict__ hist,
                                       const int32_t* __restrict__ n_keep, int T, int C) {
    const int c = blockIdx.x * blockDim.x + threadIdx.x;
    if (c >= C) return;
    int n = *n_keep;
    n = n < 0 ? 0 : (n > T ? T : n);
    if (n == 0) return;
    float e[3];
#pragma unroll
    for (int j = 0; j < 3; ++j) {
        const int i = n + j;   // entry of [hist | x_0..x_{n-1}]
        e[j] = i < 3 ? hist[(size_t)i * C + c] : x[(size_t)(i - 3) * C + c];
    }
#pragma unroll
    for (int j = 0; j < 3; ++j) hist[(size_t)j * C + c] = e[j];
}

// ------------------------------------------------------------------------------------------------ alpha / beta
__global__ void window_ab_kernel(const float* __restrict__ xin, const float* __restrict__ wab, const float* __restrict__ dt,
                                 const float* __restrict__ a, float* __restrict__ gate, float* __restrict__ beta, int D,
                                 int hv) {
    const int seg = threadIdx.x >> 5, lane = threadIdx.x & 31, t = blockIdx.y;
    const int row = blockIdx.x * 8 + seg;
    const bool live = row < 2 * hv;
    const int rc = live ? row : 2 * hv - 1;   // clamp so the whole 32-lane segment runs the reduction
    float acc = 0.f;
    for (int d = lane; d < D; d += 32) acc += xin[(size_t)t * D + d] * wab[(size_t)rc * D + d];
    acc = lane_reduce(acc);
    if (live && lane == 0) {
        if (row < hv) {
            const float z = acc + dt[row];
            const float sp = z > 20.f ? z : log1pf(expf(z));
            gate[(size_t)t * hv + row] = sp * a[row];
        } else {
            beta[(size_t)t * hv + (row - hv)] = 1.f / (1.f + expf(-acc));
        }
    }
}

// ------------------------------------------------------------------------------------------------ state step + norm
// Block per value head, RG*S = 512 threads: col = tid % S, rg = tid / S owns rows rg*RPG .. rg*RPG+RPG-1.
__global__ void __launch_bounds__(RG * S)
window_state_step_kernel(float* __restrict__ state, const float* __restrict__ q, const float* __restrict__ k,
                         const float* __restrict__ v, const float* __restrict__ gate, const float* __restrict__ beta,
                         const float* __restrict__ nw, float* __restrict__ out, int T, int hk, int hv, float scale, float eps,
                         const int32_t* __restrict__ n_keep, int t_out_begin) {
    __shared__ float sa[RG * S], sb[RG * S], so[S], sss[S / 32];
    const int head = blockIdx.x, tid = threadIdx.x;
    const int col = tid & (S - 1), rg = tid / S;
    const int qh = head % hk;
    int keep_t = n_keep ? *n_keep : 0;
    keep_t = keep_t < 0 ? 0 : (keep_t > T ? T : keep_t);
    float s[RPG];
#pragma unroll
    for (int r = 0; r < RPG; ++r) s[r] = state[((size_t)(rg * RPG + r) * hv + head) * S + col];
    for (int t = 0; t < T; ++t) {
        const float* kt = k + ((size_t)t * hk + qh) * S + rg * RPG;
        const float* qt = q + ((size_t)t * hk + qh) * S + rg * RPG;
        const float decay = expf(gate[(size_t)t * hv + head]);
        float pkv = 0.f;
#pragma unroll
        for (int r = 0; r < RPG; ++r) { s[r] *= decay; pkv += s[r] * kt[r]; }
        sa[tid] = pkv;
        __syncthreads();
        const float kv = ((sa[col] + sa[S + col]) + sa[2 * S + col]) + sa[3 * S + col];
        const float d = (v[((size_t)t * hv + head) * S + col] - kv) * beta[(size_t)t * hv + head];
        float po = 0.f;
#pragma unroll
        for (int r = 0; r < RPG; ++r) { s[r] += kt[r] * d; po += s[r] * qt[r]; }
        sb[tid] = po;
        if (keep_t > 0 && t + 1 == keep_t) {
#pragma unroll
            for (int r = 0; r < RPG; ++r) state[((size_t)(rg * RPG + r) * hv + head) * S + col] = s[r];
        }
        __syncthreads();
        if (rg == 0) so[col] = (((sb[col] + sb[S + col]) + sb[2 * S + col]) + sb[3 * S + col]) * scale;
        __syncthreads();
        if (tid < S) {
            const float val = so[tid];
            const float ss = lane_reduce(val * val);
            if ((tid & 31) == 0) sss[tid >> 5] = ss;
        }
        __syncthreads();
        if (rg == 0 && t >= t_out_begin) {
            const float ss = ((sss[0] + sss[1]) + sss[2]) + sss[3];
            out[((size_t)t * hv + head) * S + col] = so[col] * rsqrtf(ss / (float)S + eps) * nw[col];
        }
        __syncthreads();
    }
}

// ------------------------------------------------------------------------------------------------ gathers
__global__ void embedding_gather_dev_kernel(const uint8_t* __restrict__ table, const float* __restrict__ scales,
                                            const int32_t* __restrict__ ids, float* __restrict__ out, int rows, int dim,
                                            int bits, int group_elems) {
    const int row = blockIdx.x;
    const int id = ids[row];
    const bool ok = id >= 0 && id < rows;
    const uint64_t rb = ((uint64_t)dim * bits + 7) / 8;
    const uint64_t groups = (uint64_t)dim / group_elems;
    const unsigned cpb = 8u / (unsigned)bits, mask = (1u << bits) - 1u;
    const int bias = 1 << (bits - 1);
    for (int kk = threadIdx.x; kk < dim; kk += blockDim.x) {
        float val = 0.f;
        if (ok) {
            const unsigned byte = table[(uint64_t)id * rb + kk / cpb];
            const unsigned code = (byte >> ((kk % cpb) * bits)) & mask;
            val = (float)((int)code - bias) * scales[(uint64_t)id * groups + kk / group_elems];
        }
        out[(size_t)row * dim + kk] = val;
    }
}

__global__ void fetch_blobs_kernel(const uint8_t* __restrict__ src, const int32_t* __restrict__ ids, uint8_t* __restrict__ dst,
                                   int n, int nblobs, int per) {
    const uint64_t total = (uint64_t)n * per, stride = (uint64_t)gridDim.x * blockDim.x;
    for (uint64_t i = (uint64_t)blockIdx.x * blockDim.x + threadIdx.x; i < total; i += stride) {
        const uint64_t k = i / per, o = i % per;
        const int id = ids[k];
        dst[i] = (id >= 0 && id < nblobs) ? src[(uint64_t)id * per + o] : (uint8_t)0;
    }
}

__global__ void gather_rows_kernel(const uint16_t* __restrict__ src, const int32_t* __restrict__ ids,
                                   uint16_t* __restrict__ dst, int rows, int row16) {
    const int k = blockIdx.x, id = ids[k];
    const bool ok = id >= 0 && id < rows;
    for (int o = threadIdx.x; o < row16; o += blockDim.x)
        dst[(uint64_t)k * row16 + o] = ok ? src[(uint64_t)id * row16 + o] : (uint16_t)0;
}

__global__ void copy_indexed_kernel(const float* __restrict__ src, const int32_t* __restrict__ idx, float* __restrict__ dst,
                                    int stride, int nrows) {
    const int r = *idx;
    const bool ok = r >= 0 && r < nrows;
    const int step = gridDim.x * blockDim.x;
    for (int i = blockIdx.x * blockDim.x + threadIdx.x; i < stride; i += step)
        dst[i] = ok ? src[(uint64_t)r * stride + i] : 0.f;
}

__global__ void broadcast_streams_kernel(const float* __restrict__ x, float* __restrict__ R, int T, int n, int hc) {
    const uint64_t g = (uint64_t)blockIdx.x * blockDim.x + threadIdx.x;
    const uint64_t per = (uint64_t)n * hc;
    if (g < (uint64_t)T * per) R[g] = x[(g / per) * n + (g % per) % n];
}
__global__ void add_streams_broadcast_kernel(float* __restrict__ R, const float* __restrict__ x, int T, int n, int hc) {
    const uint64_t g = (uint64_t)blockIdx.x * blockDim.x + threadIdx.x;
    const uint64_t per = (uint64_t)n * hc;
    if (g < (uint64_t)T * per) R[g] += x[(g / per) * n + (g % per) % n];
}

__global__ void rebase_ptrs_kernel(uint64_t* ptrs, uint64_t base, uint64_t bytes, int n) {
    const int k = threadIdx.x;   // 128 threads
    if (k < n) ptrs[k] = base + (uint64_t)k * bytes;
}

__global__ void dense_steps_kernel(int32_t* steps, int c0, int T) {
    const int t = blockIdx.x * blockDim.x + threadIdx.x;
    if (t >= T) return;
    const int c = c0 + t;
    steps[4 * t + 0] = c;
    steps[4 * t + 1] = c + 1;
    steps[4 * t + 2] = (c + 1) / 4;
    steps[4 * t + 3] = c + 1;
}

__global__ void window_ids_kernel(int32_t* ids, int32_t* width_out, int n_kv, int window) {
    const int start = n_kv > window ? n_kv - window : 0;
    const int width = n_kv - start;
    for (int j = threadIdx.x; j < window; j += blockDim.x) ids[j] = j < width ? start + j : -1;
    if (threadIdx.x == 0) *width_out = width;
}

__global__ void map_ids_kernel(const int32_t* __restrict__ ids, const int32_t* __restrict__ map, int32_t* __restrict__ out,
                               int n, int nmap) {
    const int i = threadIdx.x;
    if (i >= n) return;
    const int id = ids[i];
    out[i] = (id >= 0 && id < nmap) ? map[id] : -1;
}

__global__ void ident_hits_kernel(const int32_t* __restrict__ ids, int n, int32_t* hits) {
    for (int i = threadIdx.x; i < n; i += blockDim.x)   // single block; block-stride over the n <= 1024 entries
        if (ids[i] == i) atomicAdd(hits, 1);
}

// ------------------------------------------------------------------------------------------------ argmax / MTP select
// Block (value, lowest index) maximum over `nthr` threads in shared memory.
__device__ __forceinline__ void argmax_tree(float* sv, int* si, int nthr) {
    for (int off = nthr / 2; off > 0; off >>= 1) {
        if ((int)threadIdx.x < off) {
            const float ov = sv[threadIdx.x + off];
            const int oi = si[threadIdx.x + off];
            if (ov > sv[threadIdx.x] || (ov == sv[threadIdx.x] && oi < si[threadIdx.x])) {
                sv[threadIdx.x] = ov;
                si[threadIdx.x] = oi;
            }
        }
        __syncthreads();
    }
}

__global__ void row_top_prob_kernel(const float* __restrict__ logits, int32_t* __restrict__ tok, float* __restrict__ prob,
                                    int vocab) {
    __shared__ float sv[kTop];
    __shared__ int si[kTop];
    __shared__ float part[kTop / 32];
    const float* row = logits + (size_t)blockIdx.x * vocab;
    float bv = -INFINITY;
    int bi = 0x7fffffff;
    for (int j = threadIdx.x; j < vocab; j += kTop)
        if (row[j] > bv) { bv = row[j]; bi = j; }
    sv[threadIdx.x] = bv;
    si[threadIdx.x] = bi;
    __syncthreads();
    argmax_tree(sv, si, kTop);
    const float mx = sv[0];
    const int ai = si[0];
    __syncthreads();
    float sum = 0.f;
    for (int j = threadIdx.x; j < vocab; j += kTop) sum += expf(row[j] - mx);
    sum = lane_reduce(sum);
    if ((threadIdx.x & 31) == 0) part[threadIdx.x >> 5] = sum;
    __syncthreads();
    if (threadIdx.x == 0) {
        float tot = 0.f;
        for (int w = 0; w < kTop / 32; ++w) tot += part[w];
        tok[blockIdx.x] = ai;
        prob[blockIdx.x] = 1.f / tot;
    }
}

__global__ void mtp_select_kernel(const int32_t* __restrict__ tok, const float* __restrict__ prob, int n,
                                  int32_t* __restrict__ sel, int32_t* __restrict__ tok_out) {
    __shared__ float sv[kBlk];
    __shared__ int si[kBlk];
    float bv = -INFINITY;
    int bi = 0x7fffffff;
    for (int j = threadIdx.x; j < n; j += kBlk)
        if (prob[j] > bv) { bv = prob[j]; bi = j; }
    sv[threadIdx.x] = bv;
    si[threadIdx.x] = bi;
    __syncthreads();
    argmax_tree(sv, si, kBlk);
    if (threadIdx.x == 0) { sel[0] = si[0]; tok_out[0] = tok[si[0]]; }
}

__global__ void wait_flag_ge_kernel(const volatile uint32_t* flag, uint32_t want, uint32_t spins, int32_t* status) {
    int32_t r = 0;
    for (uint32_t i = 0; i < spins; ++i) {
        if (*flag >= want) { r = 1; break; }
        __threadfence();
    }
    *status = r;
}

}  // namespace

int gdn_conv_l2_multi(const float* x, const float* hist, const float* w, float* out, int T, int C, int hk, int hv, float eps,
                      void* stream) {
    if (!x || !hist || !w || !out || T < 1 || (uint32_t)T > kMaxT || hk < 1 || hv < 1 || hv % hk != 0 ||
        C != S * (2 * hk + hv) || !(eps > 0.f) || !std::isfinite(eps))
        return -1;
    window_conv_kernel<<<blocks_of((uint64_t)T * C, kBlk), kBlk, 0, strm(stream)>>>(x, hist, w, out, T, C);
    window_l2_kernel<<<dim3(2 * hk, T), S, 0, strm(stream)>>>(out, C, eps);
    return check();
}
int gdn_conv_commit(const float* x, float* hist, const int32_t* n_keep_dev, int T, int C, void* stream) {
    if (!x || !hist || !n_keep_dev || T < 1 || (uint32_t)T > kMaxT || C < 1) return -1;
    gdn_conv_commit_kernel<<<blocks_of(C, kBlk), kBlk, 0, strm(stream)>>>(x, hist, n_keep_dev, T, C);
    return check();
}
int gdn_ab_multi(const float* xin, const float* wab, const float* dt, const float* a, float* gate, float* beta, int T, int D,
                 int hv, void* stream) {
    if (!xin || !wab || !dt || !a || !gate || !beta || T < 1 || (uint32_t)T > kMaxT || D < 1 || hv < 1 || hv > 32768) return -1;
    window_ab_kernel<<<dim3(blocks_of(2 * hv, 8), T), 256, 0, strm(stream)>>>(xin, wab, dt, a, gate, beta, D, hv);
    return check();
}
int gdn_step_norm_multi(float* state, const float* q, const float* k, const float* v, const float* gate, const float* beta,
                        const float* nw, float* out, int T, int hk, int hv, float scale, float eps,
                        const int32_t* n_keep_dev, int t_out_begin, void* stream) {
    if (!state || !q || !k || !v || !gate || !beta || !nw || !out) return -1;
    if (T < 1 || (uint32_t)T > kMaxT || hk < 1 || hv < 1 || hv % hk != 0 || hv > 65535 || t_out_begin < 0) return -1;
    if (!(eps >= 0.f) || !std::isfinite(eps) || !std::isfinite(scale)) return -1;
    window_state_step_kernel<<<hv, RG * S, 0, strm(stream)>>>(state, q, k, v, gate, beta, nw, out, T, hk, hv, scale, eps,
                                                              n_keep_dev, t_out_begin);
    return check();
}
int embedding_gather_dev(const uint8_t* table, const float* scales, const int32_t* ids, float* out, int n, int rows, int dim,
                         int bits, int group_elems, void* stream) {
    if (!table || !scales || !ids || !out || n < 1 || rows < 1 || dim < 1 || !bits_ok(bits) || group_elems < 1 ||
        dim % group_elems != 0)
        return -1;
    embedding_gather_dev_kernel<<<n, kBlk, 0, strm(stream)>>>(table, scales, ids, out, rows, dim, bits, group_elems);
    return check();
}
int fetch_blobs(const uint8_t* src, const int32_t* ids, uint8_t* dst, int n, int nblobs, int blob_bytes, void* stream) {
    if (!src || !ids || !dst || n < 1 || nblobs < 1 || blob_bytes < 1) return -1;
    const uint64_t total = (uint64_t)n * blob_bytes;
    const unsigned blocks = (unsigned)std::min<uint64_t>(blocks_of(total, kBlk), 4096);
    fetch_blobs_kernel<<<blocks, kBlk, 0, strm(stream)>>>(src, ids, dst, n, nblobs, blob_bytes);
    return check();
}
int gather_rows(const uint16_t* src, const int32_t* ids, uint16_t* dst, int n, int rows, int row16, void* stream) {
    if (!src || !ids || !dst || n < 1 || rows < 1 || row16 < 1) return -1;
    gather_rows_kernel<<<n, kBlk, 0, strm(stream)>>>(src, ids, dst, rows, row16);
    return check();
}
int copy_indexed(const float* src, const int32_t* idx, float* dst, int stride, int nrows, void* stream) {
    if (!src || !idx || !dst || stride < 1 || nrows < 1) return -1;
    copy_indexed_kernel<<<copy_blocks((uint64_t)stride), kBlk, 0, strm(stream)>>>(src, idx, dst, stride, nrows);
    return check();
}
int broadcast_streams(const float* x, float* R, int T, int n, int hc, void* stream) {
    if (!x || !R || T < 1 || n < 1 || hc < 1) return -1;
    broadcast_streams_kernel<<<blocks_of((uint64_t)T * n * hc, kBlk), kBlk, 0, strm(stream)>>>(x, R, T, n, hc);
    return check();
}
int add_streams_broadcast(float* R, const float* x, int T, int n, int hc, void* stream) {
    if (!x || !R || T < 1 || n < 1 || hc < 1) return -1;
    add_streams_broadcast_kernel<<<blocks_of((uint64_t)T * n * hc, kBlk), kBlk, 0, strm(stream)>>>(R, x, T, n, hc);
    return check();
}
int rebase_ptrs(uint64_t* ptrs, uint64_t base, uint64_t bytes, int n, void* stream) {
    if (!ptrs || n < 1 || n > kRebaseMax) return -1;
    rebase_ptrs_kernel<<<1, kRebaseMax, 0, strm(stream)>>>(ptrs, base, bytes, n);
    return check();
}
int dense_steps(int32_t* steps, int c0, int T, void* stream) {
    if (!steps || c0 < 0 || T < 1 || (uint32_t)T > kMaxT) return -1;
    dense_steps_kernel<<<1, kBlk, 0, strm(stream)>>>(steps, c0, T);
    return check();
}
int window_ids(int32_t* ids, int32_t* width_out, int n_kv, int window, void* stream) {
    if (!ids || !width_out || n_kv < 0 || window < 1) return -1;
    window_ids_kernel<<<1, kBlk, 0, strm(stream)>>>(ids, width_out, n_kv, window);
    return check();
}
int map_ids(const int32_t* ids, const int32_t* map, int32_t* out, int n, int nmap, void* stream) {
    if (!ids || !map || !out || n < 1 || n > kMapMax || nmap < 1) return -1;
    map_ids_kernel<<<1, kMapMax, 0, strm(stream)>>>(ids, map, out, n, nmap);
    return check();
}
int ident_hits(const int32_t* ids, int n, int32_t* hits, void* stream) {
    if (!ids || !hits || n < 1 || n > kIdentMax) return -1;
    if (cudaMemsetAsync(hits, 0, sizeof(int32_t), strm(stream)) != cudaSuccess) return check() ? check() : -1;
    ident_hits_kernel<<<1, kBlk, 0, strm(stream)>>>(ids, n, hits);
    return check();
}
int row_top_prob(const float* logits, int32_t* tok, float* prob, int rows, int vocab, void* stream) {
    if (!logits || !tok || !prob || rows < 1 || vocab < 1) return -1;
    row_top_prob_kernel<<<rows, kTop, 0, strm(stream)>>>(logits, tok, prob, vocab);
    return check();
}
int mtp_select(const int32_t* tok, const float* prob, int n, int32_t* sel, int32_t* tok_out, void* stream) {
    if (!tok || !prob || !sel || !tok_out || n < 1) return -1;
    mtp_select_kernel<<<1, kBlk, 0, strm(stream)>>>(tok, prob, n, sel, tok_out);
    return check();
}
int wait_flag_ge(const volatile uint32_t* flag, uint32_t want, uint32_t spins, int32_t* status, void* stream) {
    if (!flag || !status || spins < 1) return -1;
    wait_flag_ge_kernel<<<1, 1, 0, strm(stream)>>>(flag, want, spins, status);
    return check();
}

}  // namespace vwin
}  // namespace baloon
