import type { Route } from "../router";

export function navItemFor(r: Route): "fleet" | "errors" | "logs" | "notif" | "creds" | null {
  switch (r.name) {
    case "overview": case "detail": return "fleet";
    case "errors": return "errors";
    case "logs": return "logs";
    case "notifications": return "notif";
    case "credentials": return "creds";
    default: return null;
  }
}
