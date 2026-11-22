// launch_probe.c — preloaded into the processes of a GPU unit's test.sh (LD_PRELOAD), it
// records every GPU kernel launch the process makes, so the kernel measures that kernels
// ran on the device instead of taking "a device node was open" for it.
//
// Log lines (tab separated, appended to $EINSTEINOS_LAUNCH_LOG, one write each):
//   load    <pid> <exe>
//   launch  <pid> <api> <addr> <object base> <object path> <symbol or ->
//   device  <pid> <path>   (a /dev node the process holds open right after a launch returned)
//   out     <pid> <line>   (a "prompt:" or "generated:" line the process wrote to its stdout or stderr)
// A runtime-API launch passes the kernel's host stub, whose ELF symbol is the kernel's
// mangled name; the kernel resolves it from the object's symbol table when dladdr cannot.
// Each (api, function) is logged once per process.
#define _GNU_SOURCE
#include <dirent.h>
#include <dlfcn.h>
#include <fcntl.h>
#include <pthread.h>
#include <stdarg.h>
#include <stddef.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/syscall.h>
#include <unistd.h>

typedef int probe_err_t;
typedef struct { unsigned x, y, z; } probe_dim3;

static int probe_fd = -1;
static pthread_mutex_t probe_mu = PTHREAD_MUTEX_INITIALIZER;
#define PROBE_SEEN 1024
static const void *probe_seen[PROBE_SEEN];
static int probe_nseen;

// the probe's own writes go to the kernel directly: write() below is the probe's
static void probe_write(const char *s) {
  if (probe_fd >= 0) {
    long r = syscall(SYS_write, probe_fd, s, strlen(s));
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

// probe_devices logs, once per process, the device nodes the process holds open just after a
// launch returned: the process that ran the kernel says which device it ran on. 11/09 09:57
// and 10:15 the kernel's 50 ms sampling of /proc found no GPU device open in test processes
// that the probe had seen launch contract_kernel and partition_kernel; the next verification
// of the same code found all four.
static int probe_devices_logged;
static void probe_devices(void) {
  if (probe_fd < 0) return;
  pthread_mutex_lock(&probe_mu);
  if (probe_devices_logged) {
    pthread_mutex_unlock(&probe_mu);
    return;
  }
  probe_devices_logged = 1;
  pthread_mutex_unlock(&probe_mu);
  DIR *d = opendir("/proc/self/fd");
  if (!d) return;
  struct dirent *e;
  while ((e = readdir(d)) != NULL) {
    if (e->d_name[0] == '.') continue;
    char link[320], target[512];
    snprintf(link, sizeof link, "/proc/self/fd/%s", e->d_name);
    ssize_t n = readlink(link, target, sizeof target - 1);
    if (n <= 0) continue;
    target[n] = 0;
    if (strncmp(target, "/dev/", 5) != 0) continue;
    char line[640];
    snprintf(line, sizeof line, "device\t%d\t%s\n", (int)getpid(), target);
    probe_write(line);
  }
  closedir(d);
}

#define PROBE_REAL(name, type)                     \
  static type real_##name;                         \
  if (!real_##name) real_##name = (type)dlsym(RTLD_NEXT, #name)

// CUDA runtime API (what <<<…>>> compiles to)
typedef probe_err_t (*launch_fn)(const void *, probe_dim3, probe_dim3, void **, size_t, void *);
probe_err_t cudaLaunchKernel(const void *f, probe_dim3 g, probe_dim3 b, void **a, size_t s, void *st) {
  PROBE_REAL(cudaLaunchKernel, launch_fn);
  probe_record("cudaLaunchKernel", f);
  probe_err_t probe_r = real_cudaLaunchKernel ? real_cudaLaunchKernel(f, g, b, a, s, st) : 1;
  probe_devices();
  return probe_r;
}
probe_err_t cudaLaunchKernel_ptsz(const void *f, probe_dim3 g, probe_dim3 b, void **a, size_t s, void *st) {
  PROBE_REAL(cudaLaunchKernel_ptsz, launch_fn);
  probe_record("cudaLaunchKernel", f);
  probe_err_t probe_r = real_cudaLaunchKernel_ptsz ? real_cudaLaunchKernel_ptsz(f, g, b, a, s, st) : 1;
  probe_devices();
  return probe_r;
}
probe_err_t cudaLaunchCooperativeKernel(const void *f, probe_dim3 g, probe_dim3 b, void **a, size_t s, void *st) {
  PROBE_REAL(cudaLaunchCooperativeKernel, launch_fn);
  probe_record("cudaLaunchCooperativeKernel", f);
  probe_err_t probe_r = real_cudaLaunchCooperativeKernel ? real_cudaLaunchCooperativeKernel(f, g, b, a, s, st) : 1;
  probe_devices();
  return probe_r;
}
typedef probe_err_t (*launch_ex_fn)(const void *, const void *, void **);
probe_err_t cudaLaunchKernelExC(const void *cfg, const void *f, void **a) {
  PROBE_REAL(cudaLaunchKernelExC, launch_ex_fn);
  probe_record("cudaLaunchKernelExC", f);
  probe_err_t probe_r = real_cudaLaunchKernelExC ? real_cudaLaunchKernelExC(cfg, f, a) : 1;
  probe_devices();
  return probe_r;
}

// the pre-9.2 runtime launch (cudaConfigureCall/cudaSetupArgument/cudaLaunch), which some
// CUDA-compatible toolchains still emit
typedef probe_err_t (*legacy_launch_fn)(const void *);
probe_err_t cudaLaunch(const void *f) {
  PROBE_REAL(cudaLaunch, legacy_launch_fn);
  probe_record("cudaLaunch", f);
  probe_err_t probe_r = real_cudaLaunch ? real_cudaLaunch(f) : 1;
  probe_devices();
  return probe_r;
}

// CUDA driver API: the function is an opaque handle, logged without a name
typedef int (*cu_launch_fn)(void *, unsigned, unsigned, unsigned, unsigned, unsigned, unsigned, unsigned,
                            void *, void **, void **);
int cuLaunchKernel(void *f, unsigned gx, unsigned gy, unsigned gz, unsigned bx, unsigned by, unsigned bz,
                   unsigned shm, void *st, void **params, void **extra) {
  PROBE_REAL(cuLaunchKernel, cu_launch_fn);
  probe_record("cuLaunchKernel", f);
  probe_err_t probe_r = real_cuLaunchKernel ? real_cuLaunchKernel(f, gx, gy, gz, bx, by, bz, shm, st, params, extra) : 1;
  probe_devices();
  return probe_r;
}

// The token lines a process writes: which process printed the ids the run generated. 11/22
// 05:11-12:48 the integration's e2e.sh printed "generated:" from whatever process got that far
// (vllm in one, transformers in another, a python forward pass written into the script in the
// last), and the kernel took every id in the run's output as the port's.
#define PROBE_LINE 65536
static char probe_line[3][PROBE_LINE];
static size_t probe_line_n[3];
static int probe_line_over[3];

static int probe_token_line(const char *s, size_t n) {
  size_t i = 0;
  while (i < n && (s[i] == ' ' || s[i] == '\t')) i++;
  size_t w = 0;
  if (n - i >= 6 && memcmp(s + i, "prompt", 6) == 0) w = 6;
  else if (n - i >= 9 && memcmp(s + i, "generated", 9) == 0) w = 9;
  if (!w) return 0;
  i += w;
  while (i < n && (s[i] == ' ' || s[i] == '\t')) i++;
  return i < n && s[i] == ':';
}

static void probe_out_line(const char *s, size_t n) {
  if (n && s[n - 1] == '\r') n--;
  if (!probe_token_line(s, n)) return;
  char head[64];
  int h = snprintf(head, sizeof head, "out\t%d\t", (int)getpid());
  char *line = malloc((size_t)h + n + 2);
  if (!line) return;
  memcpy(line, head, (size_t)h);
  memcpy(line + h, s, n);
  line[h + n] = '\n';
  line[h + n + 1] = 0;
  probe_write(line);
  free(line);
}

static void probe_scan(int fd, const char *buf, size_t n) {
  pthread_mutex_lock(&probe_mu);
  char *ln = probe_line[fd];
  for (size_t i = 0; i < n; i++) {
    if (buf[i] == '\n') {
      if (!probe_line_over[fd]) probe_out_line(ln, probe_line_n[fd]);
      probe_line_n[fd] = 0;
      probe_line_over[fd] = 0;
      continue;
    }
    if (probe_line_n[fd] < PROBE_LINE) ln[probe_line_n[fd]++] = buf[i];
    else probe_line_over[fd] = 1;
  }
  pthread_mutex_unlock(&probe_mu);
}

typedef ssize_t (*write_fn)(int, const void *, size_t);
ssize_t write(int fd, const void *buf, size_t n) {
  PROBE_REAL(write, write_fn);
  if (probe_fd >= 0 && (fd == 1 || fd == 2) && buf && n) probe_scan(fd, (const char *)buf, n);
  if (!real_write) return (ssize_t)syscall(SYS_write, fd, buf, n);
  return real_write(fd, buf, n);
}

// stdio: glibc writes a FILE's buffer with its own __write, not write() above, so what a C or
// C++ program prints (printf, puts, std::cout through fwrite and putc) is read where it is
// handed to stdio
static int probe_std_fd(FILE *f) {
  if (probe_fd < 0 || !f) return 0;
  int fd = fileno(f);
  return (fd == 1 || fd == 2) ? fd : 0;
}

static void probe_vscan(FILE *f, const char *fmt, va_list ap) {
  int fd = probe_std_fd(f);
  if (!fd || !fmt) return;
  char small[4096];
  va_list cp;
  va_copy(cp, ap);
  int n = vsnprintf(small, sizeof small, fmt, cp);
  va_end(cp);
  if (n < 0) return;
  if ((size_t)n < sizeof small) {
    probe_scan(fd, small, (size_t)n);
    return;
  }
  char *big = malloc((size_t)n + 1);
  if (!big) return;
  va_copy(cp, ap);
  vsnprintf(big, (size_t)n + 1, fmt, cp);
  va_end(cp);
  probe_scan(fd, big, (size_t)n);
  free(big);
}

typedef size_t (*fwrite_fn)(const void *, size_t, size_t, FILE *);
size_t fwrite(const void *p, size_t sz, size_t nm, FILE *f) {
  PROBE_REAL(fwrite, fwrite_fn);
  int fd = probe_std_fd(f);
  if (fd) probe_scan(fd, (const char *)p, sz * nm);
  return real_fwrite(p, sz, nm, f);
}
typedef int (*fputs_fn)(const char *, FILE *);
int fputs(const char *s, FILE *f) {
  PROBE_REAL(fputs, fputs_fn);
  int fd = probe_std_fd(f);
  if (fd) probe_scan(fd, s, strlen(s));
  return real_fputs(s, f);
}
typedef int (*puts_fn)(const char *);
int puts(const char *s) {
  PROBE_REAL(puts, puts_fn);
  if (probe_std_fd(stdout)) {
    probe_scan(1, s, strlen(s));
    probe_scan(1, "\n", 1);
  }
  return real_puts(s);
}
typedef int (*fputc_fn)(int, FILE *);
#define PROBE_PUTC(name)                                  \
  int name(int c, FILE *f) {                              \
    PROBE_REAL(name, fputc_fn);                           \
    int fd = probe_std_fd(f);                             \
    if (fd) {                                             \
      char ch = (char)c;                                  \
      probe_scan(fd, &ch, 1);                             \
    }                                                     \
    return real_##name(c, f);                             \
  }
PROBE_PUTC(fputc)
PROBE_PUTC(putc)
PROBE_PUTC(_IO_putc)
typedef int (*putchar_fn)(int);
int putchar(int c) {
  PROBE_REAL(putchar, putchar_fn);
  if (probe_std_fd(stdout)) {
    char ch = (char)c;
    probe_scan(1, &ch, 1);
  }
  return real_putchar(c);
}

typedef int (*vfprintf_fn)(FILE *, const char *, va_list);
int vfprintf(FILE *f, const char *fmt, va_list ap) {
  PROBE_REAL(vfprintf, vfprintf_fn);
  probe_vscan(f, fmt, ap);
  return real_vfprintf(f, fmt, ap);
}
int vprintf(const char *fmt, va_list ap) { return vfprintf(stdout, fmt, ap); }
int fprintf(FILE *f, const char *fmt, ...) {
  va_list ap;
  va_start(ap, fmt);
  int r = vfprintf(f, fmt, ap);
  va_end(ap);
  return r;
}
int printf(const char *fmt, ...) {
  va_list ap;
  va_start(ap, fmt);
  int r = vfprintf(stdout, fmt, ap);
  va_end(ap);
  return r;
}
// _FORTIFY_SOURCE builds call these instead
typedef int (*vfprintf_chk_fn)(FILE *, int, const char *, va_list);
int __vfprintf_chk(FILE *f, int flag, const char *fmt, va_list ap) {
  PROBE_REAL(__vfprintf_chk, vfprintf_chk_fn);
  probe_vscan(f, fmt, ap);
  return real___vfprintf_chk(f, flag, fmt, ap);
}
int __vprintf_chk(int flag, const char *fmt, va_list ap) { return __vfprintf_chk(stdout, flag, fmt, ap); }
int __fprintf_chk(FILE *f, int flag, const char *fmt, ...) {
  va_list ap;
  va_start(ap, fmt);
  int r = __vfprintf_chk(f, flag, fmt, ap);
  va_end(ap);
  return r;
}
int __printf_chk(int flag, const char *fmt, ...) {
  va_list ap;
  va_start(ap, fmt);
  int r = __vfprintf_chk(stdout, flag, fmt, ap);
  va_end(ap);
  return r;
}

// what a process still holds of a token line when it exits without a newline
__attribute__((destructor)) static void probe_fini(void) {
  if (probe_fd < 0) return;
  for (int fd = 1; fd <= 2; fd++)
    if (probe_line_n[fd] && !probe_line_over[fd]) probe_out_line(probe_line[fd], probe_line_n[fd]);
}
