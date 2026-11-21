import { defineConfig, envField } from 'astro/config';
import tailwindcss from '@tailwindcss/vite';

export default defineConfig({
  output: 'static',
  vite: {
    plugins: [tailwindcss()],
    // Dev server only: `make dev` points /v1 at the edge, so the API is same-origin as in production.
    server: { proxy: { '/v1': process.env.DOJO_EDGE_URL ?? 'http://127.0.0.1:8080' } },
  },
  env: {
    schema: {
      // The public host is configuration, never page code (technical design 1.1).
      PUBLIC_ORIGIN: envField.string({ context: 'client', access: 'public', default: 'http://localhost:8080' }),
    },
  },
});
