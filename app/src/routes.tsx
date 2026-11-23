import { Navigate, type RouteObject } from 'react-router';
import { App, MachinesRoute, SettingsRoute, SignInRoute } from './App';
import { AnalyticsLayout, type PageHandle } from './components/AnalyticsLayout';

const page = (pattern: string): PageHandle => ({ page: pattern });

// Every route that should get a page view declares its pattern in `handle`; the catch-all declares none.
export const routes: RouteObject[] = [
  {
    element: <AnalyticsLayout />,
    children: [
      { path: '/', element: <App />, handle: page('/') },
      { path: '/settings', element: <SettingsRoute />, handle: page('/settings') },
      { path: '/settings/machines', element: <MachinesRoute />, handle: page('/settings/machines') },
      { path: '/signin', element: <SignInRoute />, handle: page('/signin') },
      { path: '*', element: <Navigate to="/" replace /> },
    ],
  },
];
