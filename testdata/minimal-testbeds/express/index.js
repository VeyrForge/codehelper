var createApplication = require("./lib/application");
var logger = require("./middleware/logger");

var app = createApplication();
app.use(logger);
app.use(function middleware(req, res, next) {
  next();
});

module.exports = app;
