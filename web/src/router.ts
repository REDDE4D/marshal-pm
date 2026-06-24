import { useEffect, useState } from "react";

export type Route = { name: "overview" } | { name: "detail"; agent: string; proc: string } | { name: "credentials" } | { name: "notifications" } | { name: "errors" };

export function parseHash(hash: string): Route {
  if (hash === "#/errors") return { name: "errors" };
  if (hash === "#/notifications") return { name: "notifications" };
  if (hash === "#/credentials") return { name: "credentials" };
  const m = hash.match(/^#\/a\/([^/]+)\/p\/([^/]+)$/);
  if (m) return { name: "detail", agent: decodeURIComponent(m[1]), proc: decodeURIComponent(m[2]) };
  return { name: "overview" };
}

export function useRoute(): Route {
  const [route, setRoute] = useState<Route>(() => parseHash(window.location.hash));
  useEffect(() => {
    const on = () => setRoute(parseHash(window.location.hash));
    window.addEventListener("hashchange", on);
    return () => window.removeEventListener("hashchange", on);
  }, []);
  return route;
}

export function navigate(hash: string) { window.location.hash = hash; }
export function procHref(agent: string, proc: string) {
  return `#/a/${encodeURIComponent(agent)}/p/${encodeURIComponent(proc)}`;
}
