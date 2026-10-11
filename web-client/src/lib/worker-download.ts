/** Where the Windows desktop worker is published (GitHub Releases). */
export const WORKER_RELEASES_URL = "https://github.com/Aniekan210/remote-control/releases/latest";

/**
 * The .exe attached to the latest release. GitHub serves
 * releases/latest/download/<name> straight from whichever release is
 * newest, so this only breaks if a release names the file differently —
 * set NEXT_PUBLIC_WORKER_DOWNLOAD_FILE (read at build time) to follow a rename.
 */
const WORKER_FILE =
  process.env.NEXT_PUBLIC_WORKER_DOWNLOAD_FILE?.trim() || "remoteworker-unlogged-windows-build-v2.exe";

export const WORKER_DOWNLOAD_URL = `${WORKER_RELEASES_URL}/download/${encodeURIComponent(WORKER_FILE)}`;

/** Short path that redirects to WORKER_DOWNLOAD_URL — easy to type on the PC (see next.config.ts). */
export const WORKER_DOWNLOAD_PATH = "/download";

/** What we tell people to type into the browser on their PC. */
export const WORKER_DOWNLOAD_ADDRESS = `control.aniekan.dev${WORKER_DOWNLOAD_PATH}`;

/** Phones and tablets can't run the worker, so they get the address to open on the PC instead of the .exe. */
export function isMobileUserAgent(ua: string | null): boolean {
  return /Android|iPhone|iPad|iPod|Mobile|Silk|Kindle/i.test(ua ?? "");
}
