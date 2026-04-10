import originalConfig from './vite.config';
import { mergeConfig, type UserConfig, type Plugin } from 'vite';

const resolved = typeof originalConfig === 'function'
  ? originalConfig({ command: 'build', mode: 'development' })
  : originalConfig;

const disableHmr: Plugin = {
  name: 'disable-hmr',
  enforce: 'post',
  config() {
    return { server: { hmr: false } };
  },
  configureServer(server) {
    if (server.ws && typeof server.ws.close === 'function') {
      server.ws.close();
    }
  },
};

const merged = mergeConfig(resolved as UserConfig, {
  base: '/modules/scheduler/',
  server: { allowedHosts: true, hmr: false },
});

merged.plugins = [...(Array.isArray(merged.plugins) ? merged.plugins : []), disableHmr];

export default merged;
