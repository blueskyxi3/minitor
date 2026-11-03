import { defineConfig } from 'vite';
import { fileURLToPath } from 'node:url';
import { readFileSync } from 'node:fs';

export default defineConfig({
  root: fileURLToPath(new URL('.', import.meta.url)),
  base: './',
  plugins: [{
    name: 'victoria-metrics-license',
    generateBundle() {
      this.emitFile({ type: 'asset', fileName: 'assets/licenses/VictoriaMetrics-LICENSE.txt', source: readFileSync(new URL('../licenses/VictoriaMetrics-LICENSE', import.meta.url)) });
    }
  }],
  esbuild: { jsxFactory: 'React.createElement', jsxFragment: 'React.Fragment' },
  build: {
    outDir: 'build',
    emptyOutDir: true,
    rollupOptions: {
      preserveEntrySignatures: 'strict',
      input: fileURLToPath(new URL('./src/plugin.jsx', import.meta.url)),
      output: { entryFileNames: 'assets/plugin.js', chunkFileNames: 'assets/[name]-[hash].js' }
    }
  }
});
