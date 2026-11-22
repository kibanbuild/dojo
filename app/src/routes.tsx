import { Navigate, type RouteObject } from 'react-router';
import { App, SettingsRoute, SignInRoute } from './App';

export const routes: RouteObject[] = [
  { path: '/', element: <App /> },
  { path: '/settings', element: <SettingsRoute /> },
  { path: '/signin', element: <SignInRoute /> },
  { path: '*', element: <Navigate to="/" replace /> },
];
