import type { NextConfig } from "next";
import { WORKER_DOWNLOAD_PATH, WORKER_DOWNLOAD_URL } from "./src/lib/worker-download";

const nextConfig: NextConfig = {
  images: {
    remotePatterns: [{ protocol: "https", hostname: "lh3.googleusercontent.com" }],
  },
  // control.aniekan.dev/download — a short address to type on the PC that
  // will run the worker. No sign-in needed.
  async redirects() {
    return [{ source: WORKER_DOWNLOAD_PATH, destination: WORKER_DOWNLOAD_URL, permanent: false }];
  },
};

export default nextConfig;
