import { Navigate, type RouteObject } from 'react-router';
import { App, MachinesRoute, SettingsRoute, SignInRoute } from './App';

export const routes: RouteObject[] = [
  { path: '/', element: <App /> },
  { path: '/settings', element: <SettingsRoute /> },
  { path: '/settings/machines', element: <MachinesRoute /> },
  { path: '/signin', element: <SignInRoute /> },
  { path: '*', element: <Navigate to="/" replace /> },
];
