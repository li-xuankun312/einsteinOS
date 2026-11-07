// launch_probe.c — preloaded into the processes of a GPU unit's test.sh (LD_PRELOAD), it
// records every GPU kernel launch the process makes, so the kernel measures that kernels
// ran on the device instead of taking "a device node was open" for it.
//
// Log lines (tab separated, appended to $EINSTEINOS_LAUNCH_LOG, one write each):
//   load    <pid> <exe>
//   launch  <pid> <api> <addr> <object base> <object path> <symbol or ->
// A runtime-API launch passes the kernel's host stub, whose ELF symbol is the kernel's
// mangled name; the kernel resolves it from the object's symbol table when dladdr cannot.
// Each (api, function) is logged once per process.
#define _GNU_SOURCE
#include <dlfcn.h>
#include <fcntl.h>
#include <pthread.h>
#include <stddef.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

typedef int probe_err_t;
typedef struct { unsigned x, y, z; } probe_dim3;

static int probe_fd = -1;
static pthread_mutex_t probe_mu = PTHREAD_MUTEX_INITIALIZER;
#define PROBE_SEEN 1024
static const void *probe_seen[PROBE_SEEN];
static int probe_nseen;

static void probe_write(const char *s) {
  if (probe_fd >= 0) {
    ssize_t r = write(probe_fd, s, strlen(s));
    (void)r;
  }
}

__attribute__((constructor)) static void probe_init(void) {
  const char *p = getenv("EINSTEINOS_LAUNCH_LOG");
  if (!p || !*p) return;
  probe_fd = open(p, O_WRONLY | O_APPEND | O_CREAT | O_CLOEXEC, 0644);
  char exe[1024];
  ssize_t n = readlink("/proc/self/exe", exe, sizeof exe - 1);
  if (n < 0) n = 0;
  exe[n] = 0;
  char line[1200];
  snprintf(line, sizeof line, "load\t%d\t%s\n", (int)getpid(), exe);
  probe_write(line);
}

static void probe_record(const char *api, const void *fn) {
  if (probe_fd < 0) return;
  pthread_mutex_lock(&probe_mu);
  for (int i = 0; i < probe_nseen; i++) {
    if (probe_seen[i] == fn) {
      pthread_mutex_unlock(&probe_mu);
      return;
    }
  }
  if (probe_nseen < PROBE_SEEN) probe_seen[probe_nseen++] = fn;
  pthread_mutex_unlock(&probe_mu);

  Dl_info di;
  memset(&di, 0, sizeof di);
  const char *obj = "?";
  uintptr_t base = 0;
  if (fn && dladdr(fn, &di)) {
    if (di.dli_fname && *di.dli_fname) obj = di.dli_fname;
    base = (uintptr_t)di.dli_fbase;
  }
  char line[1600];
  snprintf(line, sizeof line, "launch\t%d\t%s\t%#lx\t%#lx\t%s\t%s\n", (int)getpid(), api,
           (unsigned long)(uintptr_t)fn, (unsigned long)base, obj,
           (di.dli_sname && *di.dli_sname) ? di.dli_sname : "-");
  probe_write(line);
}

#define PROBE_REAL(name, type)                     \
  static type real_##name;                         \
  if (!real_##name) real_##name = (type)dlsym(RTLD_NEXT, #name)

// CUDA runtime API (what <<<…>>> compiles to)
typedef probe_err_t (*launch_fn)(const void *, probe_dim3, probe_dim3, void **, size_t, void *);
probe_err_t cudaLaunchKernel(const void *f, probe_dim3 g, probe_dim3 b, void **a, size_t s, void *st) {
  PROBE_REAL(cudaLaunchKernel, launch_fn);
  probe_record("cudaLaunchKernel", f);
  return real_cudaLaunchKernel ? real_cudaLaunchKernel(f, g, b, a, s, st) : 1;
}
probe_err_t cudaLaunchKernel_ptsz(const void *f, probe_dim3 g, probe_dim3 b, void **a, size_t s, void *st) {
  PROBE_REAL(cudaLaunchKernel_ptsz, launch_fn);
  probe_record("cudaLaunchKernel", f);
  return real_cudaLaunchKernel_ptsz ? real_cudaLaunchKernel_ptsz(f, g, b, a, s, st) : 1;
}
probe_err_t cudaLaunchCooperativeKernel(const void *f, probe_dim3 g, probe_dim3 b, void **a, size_t s, void *st) {
  PROBE_REAL(cudaLaunchCooperativeKernel, launch_fn);
  probe_record("cudaLaunchCooperativeKernel", f);
  return real_cudaLaunchCooperativeKernel ? real_cudaLaunchCooperativeKernel(f, g, b, a, s, st) : 1;
}
typedef probe_err_t (*launch_ex_fn)(const void *, const void *, void **);
probe_err_t cudaLaunchKernelExC(const void *cfg, const void *f, void **a) {
  PROBE_REAL(cudaLaunchKernelExC, launch_ex_fn);
  probe_record("cudaLaunchKernelExC", f);
  return real_cudaLaunchKernelExC ? real_cudaLaunchKernelExC(cfg, f, a) : 1;
}

// the pre-9.2 runtime launch (cudaConfigureCall/cudaSetupArgument/cudaLaunch), which some
// CUDA-compatible toolchains still emit
typedef probe_err_t (*legacy_launch_fn)(const void *);
probe_err_t cudaLaunch(const void *f) {
  PROBE_REAL(cudaLaunch, legacy_launch_fn);
  probe_record("cudaLaunch", f);
  return real_cudaLaunch ? real_cudaLaunch(f) : 1;
}

// CUDA driver API: the function is an opaque handle, logged without a name
typedef int (*cu_launch_fn)(void *, unsigned, unsigned, unsigned, unsigned, unsigned, unsigned, unsigned,
                            void *, void **, void **);
int cuLaunchKernel(void *f, unsigned gx, unsigned gy, unsigned gz, unsigned bx, unsigned by, unsigned bz,
                   unsigned shm, void *st, void **params, void **extra) {
  PROBE_REAL(cuLaunchKernel, cu_launch_fn);
  probe_record("cuLaunchKernel", f);
  return real_cuLaunchKernel ? real_cuLaunchKernel(f, gx, gy, gz, bx, by, bz, shm, st, params, extra) : 1;
}
