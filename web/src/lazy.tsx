// A screen loaded when it is first opened (a chunk of its own), so the first
// screen's bundle does not grow with every new one. A chunk that cannot be
// loaded (the router was upgraded under an open page, or the session ended)
// says so and offers a reload instead of a blank screen.

import { h, type ComponentType } from "preact";
import { useEffect, useState } from "preact/hooks";
import { Loading } from "./ui";

export function lazy<P extends object>(load: () => Promise<ComponentType<P>>): ComponentType<P> {
  let C: ComponentType<P> | null = null;
  let failed = false;
  let pending: Promise<void> | null = null;
  const start = () =>
    (pending ??= load().then(
      (c) => {
        C = c;
      },
      () => {
        failed = true;
        pending = null;
      },
    ));
  return function Lazy(props: P) {
    const [, redraw] = useState(0);
    useEffect(() => {
      if (C) return;
      let live = true;
      start().then(() => live && redraw((n) => n + 1));
      return () => {
        live = false;
      };
    }, []);
    if (C) return h(C, props);
    if (failed) {
      return (
        <p class="banner banner-top" role="alert">
          This part of the console could not be loaded; the router may have been upgraded.{" "}
          <button type="button" class="btn-mini" onClick={() => location.reload()}>Reload</button>
        </p>
      );
    }
    return <Loading />;
  };
}
