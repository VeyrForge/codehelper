import 'helpers.dart';
import 'tone.dart';
export 'tone.dart';

mixin Auditable {
  String audit(String s) => format(s);
}

class Greeter with Auditable {
  final Tone tone;

  Greeter({this.tone = Tone.casual});

  Greeter.formal() : tone = Tone.formal;

  String greet(String name) {
    return format(name);
  }

  String greetWithTone(String name) {
    final base = greet(name);
    return decorate(base, tone);
  }

  String get label => 'greeter';
}

/// Top-level helper — must stay parentless (not sticky Greeter ParentID).
String shout(String name) {
  return format(name);
}
