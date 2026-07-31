function createApplication() {
  var app = function () {};
  var stack = [];
  app.use = function use(fn) {
    stack.push(fn);
    return app;
  };
  app.handle = function handle(req, res) {
    var i = 0;
    function next(err) {
      if (err) {
        res.statusCode = 500;
        return;
      }
      var layer = stack[i++];
      if (!layer) {
        return;
      }
      layer(req, res, next);
    }
    next();
  };
  app._stack = stack;
  return app;
}

module.exports = createApplication;
module.exports.createApplication = createApplication;
