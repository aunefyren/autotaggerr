// Ensures this platform's esbuild binary is present before a build.
//
// esbuild ships its compiler as a native executable in a per-platform package
// (@esbuild/linux-x64, @esbuild/win32-x64, …), pulled in as an optionalDependency and
// selected by npm from the host's os/cpu. That makes node_modules platform-specific,
// which matters here because this repo is routinely built from both Windows and WSL
// against the *same* checkout: a tree installed on one has the other's binary missing,
// and vite dies with a 20-line "You installed esbuild for another platform" wall.
//
// It cannot be fixed by declaring both binaries. npm applies the os/cpu filter to
// direct dependencies too — as an optionalDependency the foreign one is silently
// skipped, and as a regular dependency it is a hard EBADPLATFORM error. Installing
// both needs either --force or a second install with --os/--cpu, neither of which
// belongs in a lockfile.
//
// So instead of pre-installing every platform, this tops up the one actually being
// used, at the moment it is needed. Properties that matter:
//
//   - It only ever *adds*. Running `npm ci` on Windows no longer makes WSL builds
//     fail (or the reverse) — the next build there repairs itself.
//   - --no-save --no-package-lock, so neither package.json nor the lockfile moves.
//     CI stays reproducible and `git status` stays clean.
//   - On the happy path (CI, and every build after the first on a given platform) it
//     is one resolve call and an exit.
//
// It deliberately does not fail the build when the install does not work: vite's own
// error is more precise about what it wanted, and a network blip should not be
// reported as a missing binary.
import { createRequire } from "node:module";
import { spawnSync } from "node:child_process";

const require = createRequire(import.meta.url);

// esbuild names its packages after process.platform/process.arch verbatim.
const target = `@esbuild/${process.platform}-${process.arch}`;

function resolves(id) {
  try {
    require.resolve(`${id}/package.json`);
    return true;
  } catch {
    return false;
  }
}

if (!resolves(target)) {
  // Match the installed esbuild exactly: the binary package is version-locked to it,
  // and a mismatched pair fails with a different, more confusing error.
  let version;
  try {
    version = require("esbuild/package.json").version;
  } catch {
    // esbuild itself is missing, so this is not the problem being solved — `npm ci`
    // is. Leave it to the build to say so.
    process.exit(0);
  }

  console.log(`Installing ${target}@${version} for this platform (node_modules was installed elsewhere)…`);
  const npm = process.platform === "win32" ? "npm.cmd" : "npm";
  const res = spawnSync(npm, ["install", "--no-save", "--no-package-lock", `${target}@${version}`], {
    stdio: "inherit",
    shell: process.platform === "win32",
  });
  if (res.status !== 0) {
    console.warn(`Could not install ${target}. The build may fail; \`npm ci\` on this platform fixes it.`);
  }
}
