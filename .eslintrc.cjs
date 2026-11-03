module.exports = {
  root: true,
  env: { browser: true, es2022: true, node: true },
  parserOptions: { ecmaVersion: 'latest', sourceType: 'module', ecmaFeatures: { jsx: true } },
  extends: ['eslint:recommended', 'plugin:react-hooks/recommended'],
  plugins: ['react'],
  rules: { 'react/jsx-uses-vars': 'error', 'react/jsx-uses-react': 'error', 'no-unused-vars': ['error', { argsIgnorePattern: '^_', varsIgnorePattern: '^React$' }] }
};
