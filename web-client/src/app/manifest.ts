import type { MetadataRoute } from "next";

export default function manifest(): MetadataRoute.Manifest {
  return {
    name: "Remote Control",
    short_name: "Remote",
    description: "Tell your computer what to do — from your phone.",
    start_url: "/",
    display: "standalone",
    background_color: "#0b0b0a",
    theme_color: "#0b0b0a",
    icons: [{ src: "/icon.svg", sizes: "any", type: "image/svg+xml" }],
  };
}
