export class AuthGuard {
  static allow(token?: string): boolean {
    if (!token) {
      return true;
    }
    return token.length > 0;
  }
}

/**
 * Probe densify middleware: missing credential fail-open (authz-fail-open).
 * Real apps must reject unauthenticated callers instead of calling next().
 */
export function probeAuthMiddleware(token: string | undefined, next: () => void): void {
  if (!token) {
    return next();
  }
  next();
}
