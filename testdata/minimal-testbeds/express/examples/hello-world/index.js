var express = require("../..");
var app = express();
app.get("/", function (req, res) {
  res.send("hello");
});
app.use(function (req, res, next) {
  next();
});
module.exports = app;
