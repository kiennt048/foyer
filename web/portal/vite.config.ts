import preact from "@preact/preset-vite";
import { defineConfig } from "vite";
import { portalDev } from "./plugin/dev.js";

export default defineConfig({
  plugins: [preact(), portalDev()],
  server: {
    proxy: {
      // Same-origin from the browser's point of view, which is what the portal has
      // in production — it is served by the Go binary that also answers these.
      "/api": {
        target: process.env.MOCK_ORIGIN ?? "http://localhost:5170",
        changeOrigin: false,
      },
      /*
       * The venue photograph, served by the mock and reached at the same origin.
       *
       * `HeroImage.url` is root-relative because in production the Go binary serves
       * both the portal and the asset — that is what makes it reachable before a
       * guest has internet, since the portal's own FQDN is already required to be in
       * the walled garden. In development the two are different ports, so the proxy
       * is what keeps the *path* honest: without it `/venue/...` would have to become
       * an absolute URL here and nowhere else, and the one environment anybody
       * reviews in would be the one where the rule does not hold.
       */
      "/venue": {
        target: process.env.MOCK_ORIGIN ?? "http://localhost:5170",
        changeOrigin: false,
      },
    },
  },
});
