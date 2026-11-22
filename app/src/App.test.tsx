import { renderToString } from 'react-dom/server';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { describe, expect, it } from 'vitest';
import { routes } from './routes';

describe('shell', () => {
  it('renders the placeholder at the app root', () => {
    const html = renderToString(<RouterProvider router={createMemoryRouter(routes)} />);
    expect(html).toContain('Dojo app');
  });
});
