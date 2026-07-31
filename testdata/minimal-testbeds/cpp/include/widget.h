#ifndef WIDGET_H
#define WIDGET_H

namespace ui {

class Widget {
public:
  void draw();
  void resize(int w, int h);

private:
  int width_ = 0;
  int height_ = 0;
};

}  // namespace ui

#endif
