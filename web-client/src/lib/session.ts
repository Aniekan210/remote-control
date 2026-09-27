import "server-only";
import { headers } from "next/headers";
import { redirect } from "next/navigation";
import { auth } from "./auth";

export async function getSession() {
  return auth.api.getSession({ headers: await headers() });
}

/**
 * Use in server components / actions that need a signed-in user.
 * `returnTo` brings the user back after Google sign-in (e.g. a scanned
 * /settings?device=… link opened on a phone that isn't signed in yet).
 */
export async function requireSession(returnTo?: string) {
  const session = await getSession();
  if (!session) redirect(returnTo ? `/login?next=${encodeURIComponent(returnTo)}` : "/login");
  return session;
}

/** Only allow same-site relative paths as post-login destinations. */
export function safeNext(next: string | undefined): string {
  return next && next.startsWith("/") && !next.startsWith("//") ? next : "/";
}
