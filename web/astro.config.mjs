import { defineConfig, envField } from 'astro/config';
import tailwindcss from '@tailwindcss/vite';

export default defineConfig({
  output: 'static',
  vite: {
    plugins: [tailwindcss()],
    // Dev server only, so the page, the API and the SPA share one origin as they do behind Caddy:
    // `make dev` points /v1 at the edge and /app at the SPA's Vite dev server (HMR over the same proxy).
    server: {
      proxy: {
        '/v1': process.env.DOJO_EDGE_URL ?? 'http://127.0.0.1:8080',
        '^/app(/|\\?|$)': { target: process.env.DOJO_SPA_URL ?? 'http://127.0.0.1:5174', ws: true },
      },
    },
  },
  env: {
    schema: {
      // The public host is configuration, never page code (technical design 1.1).
      PUBLIC_ORIGIN: envField.string({ context: 'client', access: 'public', default: 'http://localhost:8080' }),
    },
  },
});
