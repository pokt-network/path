# Profile-Guided Optimization (PGO)

Go's compiler can use a production CPU profile to optimize the build — mainly
more aggressive inlining of hot functions and devirtualization of hot interface
calls (PATH uses interfaces heavily: `endpoint`, QoS, `ReputationService`).
Reported gains are typically ~2–14% CPU, whole-binary, on top of any hand
tuning.

## How it's wired

Go automatically applies a file named **`default.pgo`** located in the main
package directory. PATH always builds `./cmd`, so the profile lives at:

```
cmd/default.pgo
```

Every build path (`make path_build`, release builds, all Dockerfiles) runs
`go build ... ./cmd` and `COPY . .`, so a committed `cmd/default.pgo` is picked
up automatically — no flags, no Dockerfile or Makefile changes required.

`cmd/default.pgo` is committed to the repo: it is a build input, not an
artifact, so CI/local/Docker builds all use the same profile.

## Installing / refreshing the profile

1. Capture a CPU profile from a **busy, representative** prod/mainnet pod
   (30–60s under real traffic), e.g. via the pprof endpoint:

   ```
   curl -o cpu.prof "http://<pod>:<pprof-port>/debug/pprof/profile?seconds=30"
   ```

2. Install it (validates it parses, copies to `cmd/default.pgo`):

   ```
   make pgo_install PROFILE=/path/to/cpu.prof
   ```

3. Verify the build applies it, then commit:

   ```
   make pgo_verify   # expects "-pgoprofile present"
   git add cmd/default.pgo && git commit -m "build: refresh PGO profile"
   ```

`make pgo_status` shows whether a profile is currently installed.

## Notes

- The profile must reflect **real traffic**. A skewed or idle profile only dulls
  the benefit; it never affects correctness.
- Refresh every few releases or after a significant traffic-shape change.
- A stale profile is safe — worst case the optimization is less effective.
- Build time is slightly longer with PGO enabled.
