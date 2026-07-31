import 'tone.dart';

String format(String s) {
  return s;
}

String decorate(String s, Tone tone) {
  if (tone == Tone.formal) {
    return 'Dear $s';
  }
  return Helpers.tag(s);
}

class Helpers {
  static String tag(String s) {
    return '[$s]';
  }
}
