// Ensures this platform's native binaries are present before a build.
//
// vite bundles with rolldown and compiles CSS with lightningcss. Both ship their
// compiler as a native executable in a per-platform package
// (@rolldown/binding-linux-x64-gnu, lightningcss-win32-x64-msvc, …), pulled in as an
// optionalDependency and selected by npm from the host's os/cpu. That makes
// node_modules platform-specific, which matters here because this repo is routinely
// built from both Windows and WSL against the *same* checkout: a tree installed on one
// has the other's binaries missing, and the build dies in a wall of napi stack frames.
//
// It cannot be fixed by declaring every binary. npm applies the os/cpu filter to direct
// dependencies too — as an optionalDependency the foreign one is silently skipped, and
// as a regular dependency it is a hard EBADPLATFORM error. Installing both needs either
// --force or a second install with --os/--cpu, neither of which belongs in a lockfile.
//
// So instead of pre-installing every platform, this tops up the ones actually being
// used, at the moment they are needed. Properties that matter:
//
//   - It only ever *adds*. Running `npm ci` on Windows no longer makes WSL builds fail
//     (or the reverse) — the next build there repairs itself.
//   - --no-save --no-package-lock, so neither package.json nor the lockfile moves. CI
//     stays reproducible and `git status` stays clean.
//   - On the happy path (CI, and every build after the first on a given platform) it is
//     two existence checks and an exit.
//
// It deliberately does not fail the build when an install does not work: the bundler's
// own error is more precise about what it wanted, and a network blip should not be
// reported as a missing binary.
import { existsSync, readFileSync } from "node:fs";
import { spawnSync } from "node:child_process";

// Resolved from this file rather than the cwd, and read off disk rather than through
// require.resolve: lightningcss does not export its own package.json, so asking the
// resolver for it fails even when the package is sitting right there — which would make
// this script skip lightningcss silently and look like it had worked.
const modules = new URL("../node_modules/", import.meta.url);

// Both packages name their binaries after the same platform-arch-abi triple that napi
// generates, so one string serves both.
function platformTriple() {
  const base = `${process.platform}-${process.arch}`;
  if (process.platform === "win32") return `${base}-msvc`;
  if (process.platform === "linux") {
    if (process.arch === "arm") return `${base}-gnueabihf`;
    // glibcVersionRuntime is absent on musl, which needs the other binary entirely.
    return process.report?.getReport()?.header?.glibcVersionRuntime
      ? `${base}-gnu`
      : `${base}-musl`;
  }
  return base; // darwin, freebsd, android: no abi suffix
}

// Parent package -> the name its per-platform binary package goes by.
const natives = [
  { parent: "rolldown", binary: (triple) => `@rolldown/binding-${triple}` },
  { parent: "lightningcss", binary: (triple) => `lightningcss-${triple}` },
];

const installed = (name) => existsSync(new URL(`${name}/`, modules));

function installedVersion(name) {
  try {
    return JSON.parse(readFileSync(new URL(`${name}/package.json`, modules), "utf8")).version;
  } catch {
    return null;
  }
}

const triple = platformTriple();
const missing = [];

for (const { parent, binary } of natives) {
  const target = binary(triple);
  if (installed(target)) continue;

  // Match the installed parent exactly: the binary package is version-locked to it, and
  // a mismatched pair fails with a different, more confusing error.
  const version = installedVersion(parent);
  if (version === null) {
    // The parent itself is missing, so this is not the problem being solved — `npm ci`
    // is. Leave it to the build to say so.
    continue;
  }
  missing.push(`${target}@${version}`);
}

if (missing.length > 0) {
  console.log(
    `Installing ${missing.join(", ")} for this platform (node_modules was installed elsewhere)…`,
  );
  const npm = process.platform === "win32" ? "npm.cmd" : "npm";
  const res = spawnSync(npm, ["install", "--no-save", "--no-package-lock", ...missing], {
    stdio: "inherit",
    shell: process.platform === "win32",
  });
  if (res.status !== 0) {
    console.warn(
      `Could not install ${missing.join(", ")}. The build may fail; \`npm ci\` on this platform fixes it.`,
    );
  }
}
