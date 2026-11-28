// verify_commit_dev.cu - device-side commit rows and accepted prefix of the verify window (see verify_commit_dev.hpp).
#include <cuda_runtime.h>
#include <cstdint>

#include "baloon/verify_commit_dev.hpp"

namespace baloon {
namespace vcommit {

__global__ void commit_rows_kernel(int32_t* out, const int32_t* n_keep_dev, int pos0, int last_t) {
    int n = *n_keep_dev;
    n = n < 0 ? 0 : (n > last_t ? last_t : n);
    for (int t = threadIdx.x; t < last_t + 2; t += blockDim.x) {
        int32_t v;
        if (t == 0) v = n;
        else if (t == 1) v = n - 1;
        else v = (t - 2) < n ? pos0 + (t - 2) : -1;
        out[t] = v;
    }
}

// One block: each thread finds the first mismatch among its strided indices, then a shared-memory min picks the global first.
__global__ void accept_prefix_kernel(const int32_t* draft, const int32_t* target, int n_draft, int32_t* n_keep, int32_t* accepted) {
    __shared__ int first_bad;
    if (threadIdx.x == 0) first_bad = n_draft;
    __syncthreads();
    for (int i = threadIdx.x; i < n_draft; i += blockDim.x)
        if (draft[i] != target[i]) atomicMin(&first_bad, i);
    __syncthreads();
    if (threadIdx.x == 0) { *accepted = first_bad; *n_keep = first_bad + 1; }
}

int commit_rows(int32_t* out, const int32_t* n_keep_dev, int pos0, int last_t, void* stream) {
    if (!out || !n_keep_dev || last_t < 0 || last_t > kMaxRows) return -1;
    commit_rows_kernel<<<1, 256, 0, (cudaStream_t)stream>>>(out, n_keep_dev, pos0, last_t);
    return (int)cudaGetLastError();
}

int accept_prefix(const int32_t* draft, const int32_t* target, int n_draft, int32_t* n_keep, int32_t* accepted, void* stream) {
    if (!n_keep || !accepted || n_draft < 0 || n_draft > kMaxRows || (n_draft > 0 && (!draft || !target))) return -1;
    accept_prefix_kernel<<<1, 256, 0, (cudaStream_t)stream>>>(draft, target, n_draft, n_keep, accepted);
    return (int)cudaGetLastError();
}

}  // namespace vcommit
}  // namespace baloon
