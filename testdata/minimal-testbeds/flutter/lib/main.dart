import 'package:flutter/material.dart';
import 'package:go_router/go_router.dart';

import 'screens/detail_screen.dart';
import 'screens/home_screen.dart';

final GoRouter appRouter = GoRouter(
  routes: [
    GoRoute(
      path: '/',
      name: 'home',
      builder: (context, state) => const HomeScreen(),
    ),
    GoRoute(
      path: '/detail',
      name: 'detail',
      builder: (context, state) => const DetailScreen(),
    ),
  ],
);

class MyApp extends StatelessWidget {
  const MyApp({super.key});

  @override
  Widget build(BuildContext context) {
    return MaterialApp.router(
      title: 'Flutter Probe',
      routerConfig: appRouter,
    );
  }
}

void main() {
  runApp(const MyApp());
}
