// The polling schedule, apart from Preact so it can be tested with a fake
// clock. One request at a time: the next poll is scheduled only after the last
// one settles, so a slow router is waited for instead of having every request
// aborted by the next tick and never answering. A request is aborted only when
// the screen goes away or asks for something else (dispose).

export interface Clock {
  setTimeout(fn: () => void, ms: number): unknown;
  clearTimeout(handle: unknown): void;
}

export interface PollerOptions<T> {
  load: (signal: AbortSignal) => Promise<T>;
  /** Interval between one answer and the next request; 0 loads once. */
  everyMs: number;
  clock: Clock;
  /** Whether the page is hidden now; a hidden page polls nothing. */
  hidden: () => boolean;
  onData: (v: T) => void;
  onError: (e: unknown) => void;
}

export interface Poller {
  start(): void;
  /** The page was hidden: no new requests; one in flight may finish. */
  pause(): void;
  /** The page was shown: load now unless a request is already in flight. */
  resume(): void;
  /** Abort what is in flight and schedule nothing more. */
  dispose(): void;
}

export function createPoller<T>(o: PollerOptions<T>): Poller {
  let ctl: AbortController | null = null; // the request in flight, if any
  let timer: unknown = null;
  let paused = false;
  let disposed = false;
  let loadedOnce = false;

  const clear = () => {
    if (timer !== null) o.clock.clearTimeout(timer);
    timer = null;
  };
  const schedule = () => {
    clear();
    if (disposed || paused || o.everyMs <= 0) return;
    timer = o.clock.setTimeout(run, o.everyMs);
  };
  function run() {
    timer = null;
    if (disposed || paused || ctl) return;
    // The page may have turned still (hidden, paused) with no event to say
    // so: then wait for resume() instead of asking.
    if (loadedOnce && o.hidden()) {
      paused = true;
      return;
    }
    const mine = new AbortController();
    ctl = mine;
    o.load(mine.signal).then(
      (v) => {
        if (disposed || mine.signal.aborted) return;
        loadedOnce = true;
        o.onData(v);
      },
      (e) => {
        if (disposed || mine.signal.aborted) return;
        o.onError(e);
      },
    ).finally(() => {
      if (ctl === mine) ctl = null;
      schedule();
    });
  }

  return {
    start() {
      paused = o.hidden();
      if (!paused) run();
    },
    pause() {
      paused = true;
      clear();
    },
    resume() {
      if (disposed) return;
      paused = false;
      if (ctl) return; // its answer schedules the next poll
      if (o.everyMs <= 0 && loadedOnce) return;
      run();
    },
    dispose() {
      disposed = true;
      clear();
      ctl?.abort();
      ctl = null;
    },
  };
}
