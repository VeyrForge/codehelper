var express = require("../..");
var app = express();
app.use(function auth(req, res, next) {
  next();
});
module.exports = app;
