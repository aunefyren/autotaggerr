import { computeLayout, LayoutInput } from "./graphLayout";

// Typed by hand rather than with lib "webworker", which clashes with the DOM lib the
// rest of the app compiles against.
const ctx = self as unknown as {
  onmessage: ((e: MessageEvent<{ job: number; input: LayoutInput }>) => void) | null;
  postMessage(message: unknown, transfer: Transferable[]): void;
};

// One job per message; the caller ignores answers to a job it has moved past.
ctx.onmessage = (e) => {
  const positions = computeLayout(e.data.input);
  ctx.postMessage({ job: e.data.job, positions }, [positions.buffer]);
};
