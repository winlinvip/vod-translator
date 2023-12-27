const { createProxyMiddleware } = require('http-proxy-middleware');

console.log('setupProxy for development reactjs');

// See https://create-react-app.dev/docs/proxying-api-requests-in-development/
// See https://create-react-app.dev/docs/proxying-api-requests-in-development/#configuring-the-proxy-manually
module.exports = function(app) {
  app.use('/api/vod-translator/', createProxyMiddleware({target: 'http://127.0.0.1:3001/'}));
};
