import { defineConfig, envField } from 'astro/config';
import tailwindcss from '@tailwindcss/vite';

export default defineConfig({
  output: 'static',
  vite: { plugins: [tailwindcss()] },
  env: {
    schema: {
      // The public host is configuration, never page code (technical design 1.1).
      PUBLIC_ORIGIN: envField.string({ context: 'client', access: 'public', default: 'http://localhost:8080' }),
    },
  },
});
