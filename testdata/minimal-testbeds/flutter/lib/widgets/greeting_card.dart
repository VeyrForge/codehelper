import 'package:flutter/material.dart';

class GreetingCard extends StatelessWidget {
  const GreetingCard({super.key, required this.title, this.onOpenDetail});

  final String title;
  final VoidCallback? onOpenDetail;

  @override
  Widget build(BuildContext context) {
    return Column(
      mainAxisSize: MainAxisSize.min,
      children: [
        Text(title),
        if (onOpenDetail != null)
          TextButton(onPressed: onOpenDetail, child: const Text('Open')),
      ],
    );
  }
}
