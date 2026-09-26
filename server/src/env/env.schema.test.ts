import { describe, expect, it } from 'vitest';
import { envSchema } from './env.schema';

describe('optional tracker configuration', () => {
  it('supports no trackers and preserves legacy nCore configuration', () => {
    expect(envSchema.parse({ ADDON_DIR: '/tmp' }).NCORE_ENABLED).toBe(false);
    expect(
      envSchema.parse({
        ADDON_DIR: '/tmp',
        NCORE_USERNAME: 'user',
        NCORE_PASSWORD: 'pass',
      }).NCORE_ENABLED,
    ).toBe(true);
  });
  it('supports BitHUmen alone and explicit disabling with credentials present', () => {
    const config = envSchema.parse({
      ADDON_DIR: '/tmp',
      BITHUMEN_COOKIE: 'uid=123; pass=test',
      NCORE_ENABLED: 'false',
    });
    expect(config.BITHUMEN_ENABLED).toBe(true);
    expect(config.NCORE_ENABLED).toBe(false);
    expect(
      envSchema.parse({ ...config, NCORE_ENABLED: 'false', BITHUMEN_ENABLED: 'false' })
        .BITHUMEN_ENABLED,
    ).toBe(false);
  });
  it('rejects enabled trackers without credentials and invalid booleans', () => {
    for (const values of [
      { NCORE_ENABLED: 'true' },
      { BITHUMEN_ENABLED: 'true' },
      { NCORE_ENABLED: 'yes' },
      { BITHUMEN_COOKIE: 'a=b\r\nInjected: header' },
    ]) {
      expect(envSchema.safeParse({ ADDON_DIR: '/tmp', ...values }).success).toBe(false);
    }
  });
  it('enables both trackers independently', () => {
    const config = envSchema.parse({
      ADDON_DIR: '/tmp',
      NCORE_USERNAME: 'user',
      NCORE_PASSWORD: 'pass',
      BITHUMEN_COOKIE: 'uid=123',
    });
    expect(config.NCORE_ENABLED && config.BITHUMEN_ENABLED).toBe(true);
  });
});
