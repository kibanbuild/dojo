import { Navigate, type RouteObject } from 'react-router';
import { App, SignInRoute } from './App';

export const routes: RouteObject[] = [
  { path: '/', element: <App /> },
  { path: '/signin', element: <SignInRoute /> },
  { path: '*', element: <Navigate to="/" replace /> },
];
