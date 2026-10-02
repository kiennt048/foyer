import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
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
  test: {
    /*
     * Vitest replaces CSS with an empty string by default, `?raw` included.
     *
     * That is the right default for a component test and the wrong one here:
     * `gallery.test.tsx` asserts things *about the stylesheet* — that every status
     * level has a pill rule and a notice rule, that the reduced-motion block keeps
     * the skeleton and the heartbeat running, that no transition survived the motion
     * pass. Stubbed to `""` those assertions do not fail, they pass vacuously, which
     * is the worst of the three outcomes.
     */
    css: true,
  },
});
